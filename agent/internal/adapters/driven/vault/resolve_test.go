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
