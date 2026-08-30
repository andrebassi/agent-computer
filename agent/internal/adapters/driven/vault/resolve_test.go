package vault

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubGetter dubla o cofre para exercitar a ordem de resolução.
type stubGetter struct {
	value string
	err   error
	calls int
}

// Get devolve o que o dublê foi configurado para devolver.
func (s *stubGetter) Get(_ context.Context, _ string) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.value, nil
}

// O cofre VENCE o ambiente quando os dois têm valor.
//
// É a garantia central: se o ambiente ganhasse, provisionar o cofre não mudaria
// nada e a migração seria só aparência.
func TestResolvePrefersVaultOverEnvironment(t *testing.T) {
	t.Setenv("XAI_API_KEY", "valor-do-ambiente")
	store := &stubGetter{value: "valor-do-cofre"}
	value, source, err := Resolve(context.Background(), store, "bassi/xai/apikey", "XAI_API_KEY")
	if err != nil {
		t.Fatalf("resolução falhou: %v", err)
	}
	if value != "valor-do-cofre" {
		t.Fatalf("o cofre devia vencer: %q", value)
	}
	if source != SourceVault {
		t.Fatalf("origem errada: %q", source)
	}
}

// Sem cofre, o ambiente atende — e a origem denuncia a degradação.
//
// A origem é o que separa "está usando o cofre" de "caiu para o ambiente" no
// log de subida. Sem ela as duas máquinas pareceriam idênticas.
func TestResolveFallsBackToEnvironmentAndReportsIt(t *testing.T) {
	t.Setenv("XAI_API_KEY", "valor-do-ambiente")
	value, source, err := Resolve(context.Background(), nil, "bassi/xai/apikey", "XAI_API_KEY")
	if err != nil {
		t.Fatalf("resolução falhou: %v", err)
	}
	if value != "valor-do-ambiente" || source != SourceEnv {
		t.Fatalf("esperava o ambiente: %q de %q", value, source)
	}
}

// Chave ausente no cofre cai para o ambiente sem virar erro.
func TestResolveFallsBackWhenKeyMissingFromVault(t *testing.T) {
	t.Setenv("XAI_API_KEY", "valor-do-ambiente")
	store := &stubGetter{err: ErrSecretNotFound}
	value, source, err := Resolve(context.Background(), store, "bassi/xai/apikey", "XAI_API_KEY")
	if err != nil || value != "valor-do-ambiente" || source != SourceEnv {
		t.Fatalf("devia cair para o ambiente: %q, %q, %v", value, source, err)
	}
	if store.calls != 1 {
		t.Fatalf("o cofre devia ser consultado uma vez, foram %d", store.calls)
	}
}

// Cofre ilegível E ambiente vazio falha, e a causa estrutural sobrevive.
//
// Sem preservar a causa, a mensagem diria só "ausente" — e mandaria gravar um
// segredo que já está lá, quando o problema é a identidade age.
func TestResolveKeepsStructuralCauseWhenBothFail(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	cause := errors.New("identidade age ilegível")
	store := &stubGetter{err: cause}
	_, _, err := Resolve(context.Background(), store, "bassi/xai/apikey", "XAI_API_KEY")
	if !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("esperava ErrSecretUnavailable, veio %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("a causa estrutural devia sobreviver: %v", err)
	}
}

// Sem cofre e sem ambiente, a mensagem aponta o script que resolve.
func TestResolveTellsHowToFixWhenNothingIsAvailable(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	_, _, err := Resolve(context.Background(), nil, "bassi/xai/apikey", "XAI_API_KEY")
	if !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("esperava ErrSecretUnavailable, veio %v", err)
	}
	if !strings.Contains(err.Error(), "26-setup-vault.sh") {
		t.Fatalf("a mensagem devia dizer como consertar: %v", err)
	}
}

// Um *Gopass NULO chegando como interface NÃO derruba o processo.
//
// É a armadilha clássica do Go, e ela custou um `panic` em produção: `openVault`
// devolve nil quando o cofre não abre — comportamento deliberado, para cair
// para o ambiente —, mas um ponteiro nulo atribuído a uma interface produz uma
// interface NÃO-nula. O `store != nil` daqui passa, o método é chamado, e o
// binário morre com nil pointer dereference.
//
// Medido em 30/08/2026: quatro perguntas seguidas do teste de busca derrubaram
// o agentd, porque ele rodava como `agent` — que não lê o cofre por desenho.
//
// Nenhum teste anterior pegou porque todos passavam um dublê de verdade. Este
// reproduz o caminho real: o tipo concreto, nulo, virando interface.
func TestResolveSurvivesTypedNilVault(t *testing.T) {
	t.Setenv("XAI_API_KEY", "valor-do-ambiente")
	var semCofre *Gopass // nulo, e vira interface NÃO-nula ao ser passado
	value, source, err := Resolve(context.Background(), semCofre, "bassi/xai/apikey", "XAI_API_KEY")
	if err != nil {
		t.Fatalf("um cofre nulo devia degradar para o ambiente, veio %v", err)
	}
	if value != "valor-do-ambiente" || source != SourceEnv {
		t.Fatalf("esperava o ambiente: %q de %q", value, source)
	}
}

// E sem ambiente também, o cofre nulo falha com erro — nunca com pânico.
func TestResolveWithTypedNilAndNoEnvFailsCleanly(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	var semCofre *Gopass
	if _, _, err := Resolve(context.Background(), semCofre, "k", "XAI_API_KEY"); err == nil {
		t.Fatal("devia falhar, e falhar com erro em vez de pânico")
	}
}

// Valor do ambiente com espaço em volta é aparado.
//
// Um "\n" no fim vem de qualquer editor e produziria cabeçalho Authorization
// inválido — falha que parece chave errada quando é só o arquivo.
func TestResolveTrimsEnvironmentValue(t *testing.T) {
	t.Setenv("XAI_API_KEY", "  chave-com-espaco\n")
	value, _, err := Resolve(context.Background(), nil, "bassi/xai/apikey", "XAI_API_KEY")
	if err != nil {
		t.Fatalf("resolução falhou: %v", err)
	}
	if value != "chave-com-espaco" {
		t.Fatalf("devia aparar: %q", value)
	}
}
