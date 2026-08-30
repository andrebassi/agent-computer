package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStore cria um armazenamento num diretório temporário do teste.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore falhou: %v", err)
	}
	return s, dir
}

// O diretório é criado na construção, senão a primeira habilidade falharia num
// computador recém-reconstruído.
func TestNewStoreCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fundo", "do", "poco")
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore devia criar a árvore: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("diretório não foi criado: %v", err)
	}
}

// Ida e volta de uma habilidade.
func TestSaveAndGet(t *testing.T) {
	s, _ := newStore(t)
	conteudo := "1. rode os testes\n2. publique a tag\n3. avise no canal"
	if err := s.Save("release", conteudo); err != nil {
		t.Fatalf("Save falhou: %v", err)
	}
	got, err := s.Get("release")
	if err != nil {
		t.Fatalf("Get falhou: %v", err)
	}
	if got != conteudo {
		t.Fatalf("conteúdo voltou diferente: %q", got)
	}
}

// O nome vem do texto que a pessoa digitou, então precisa ser validado antes de
// virar caminho de arquivo.
func TestInvalidNamesAreRejected(t *testing.T) {
	s, _ := newStore(t)
	invalidos := []string{
		"",
		"com espaço",
		"com/barra",
		"../fuga",
		"../../etc/passwd",
		"acentuação",
		strings.Repeat("x", 49),
	}
	for _, nome := range invalidos {
		t.Run(nome, func(t *testing.T) {
			if err := s.Save(nome, "conteúdo"); err == nil {
				t.Fatalf("Save devia recusar %q", nome)
			}
			if _, err := s.Get(nome); err == nil {
				t.Fatalf("Get devia recusar %q", nome)
			}
			if err := s.Remove(nome); err == nil {
				t.Fatalf("Remove devia recusar %q", nome)
			}
		})
	}
}

// Nome com subida de diretório não pode gravar fora da pasta de habilidades.
func TestSaveCannotEscapeDirectory(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Save("../fuga", "conteúdo"); err == nil {
		t.Fatal("nome com subida de diretório devia ser recusado")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "fuga.md")); !os.IsNotExist(err) {
		t.Fatal("nenhum arquivo devia ter sido criado fora do diretório")
	}
}

// Conteúdo vazio marcaria a habilidade como existente e injetaria nada no
// prompt, o que confunde mais que a ausência.
func TestSaveRejectsEmptyContent(t *testing.T) {
	s, _ := newStore(t)
	for _, conteudo := range []string{"", "   ", "\n\t\n"} {
		if err := s.Save("vazia", conteudo); err == nil {
			t.Fatalf("conteúdo %q devia ser recusado", conteudo)
		}
	}
}

// O conteúdo entra no prompt a cada iteração, então o limite protege custo, não
// disco.
func TestSaveRejectsOversizedContent(t *testing.T) {
	s, _ := newStore(t)
	err := s.Save("gigante", strings.Repeat("a", maxSkillBytes+1))
	if err == nil {
		t.Fatal("conteúdo acima do limite devia ser recusado")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("a mensagem devia explicar o motivo do limite: %v", err)
	}
	// Exatamente no limite ainda passa: um limite que recusa o valor de fronteira
	// é uma surpresa desnecessária.
	if err := s.Save("no-limite", strings.Repeat("a", maxSkillBytes)); err != nil {
		t.Fatalf("conteúdo no limite devia ser aceito: %v", err)
	}
}

// Habilidade inexistente produz erro nomeado, para quem chama poder avisar.
func TestGetAndRemoveReportMissingSkill(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Get("nunca-criada"); err == nil {
		t.Fatal("Get de habilidade inexistente devia falhar")
	}
	if err := s.Remove("nunca-criada"); err == nil {
		t.Fatal("Remove de habilidade inexistente devia falhar")
	}
}

// Remover tira do disco.
func TestRemove(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Save("temporaria", "conteúdo"); err != nil {
		t.Fatalf("Save falhou: %v", err)
	}
	if err := s.Remove("temporaria"); err != nil {
		t.Fatalf("Remove falhou: %v", err)
	}
	if _, err := s.Get("temporaria"); err == nil {
		t.Fatal("habilidade removida não devia ser encontrada")
	}
}

// A listagem é ordenada, e ignora o que não for habilidade.
func TestListIsSortedAndIgnoresOtherFiles(t *testing.T) {
	s, dir := newStore(t)
	for _, nome := range []string{"zeta", "alpha", "meio"} {
		if err := s.Save(nome, "conteúdo"); err != nil {
			t.Fatalf("Save falhou: %v", err)
		}
	}
	// Um arquivo de outra extensão no diretório não pode virar habilidade.
	if err := os.WriteFile(filepath.Join(dir, "anotacao.txt"), []byte("nota"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "uma-pasta"), 0o755); err != nil {
		t.Fatalf("mkdir falhou: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List falhou: %v", err)
	}
	want := []string{"alpha", "meio", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("listagem inesperada: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordem instável: %v, esperava %v", got, want)
		}
	}
}

// Diretório ausente devolve lista vazia sem erro: é o estado de um computador
// onde ninguém salvou habilidade ainda.
func TestListHandlesMissingDirectory(t *testing.T) {
	s, dir := newStore(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remoção falhou: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("diretório ausente não devia ser erro: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("esperava lista vazia, veio %v", got)
	}
}

// O bloco expandido precisa ser delimitado e nomeado: sem isso, um procedimento
// longo se mistura à tarefa e o modelo passa a tratá-lo como o objetivo.
func TestExpandDelimitsEachSkill(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Save("release", "publique a tag"); err != nil {
		t.Fatalf("Save falhou: %v", err)
	}
	bloco, missing := s.Expand([]string{"release"})
	if len(missing) != 0 {
		t.Fatalf("nada devia faltar: %v", missing)
	}
	if !strings.Contains(bloco, "habilidade salva: release") {
		t.Fatalf("o bloco devia nomear a habilidade: %q", bloco)
	}
	if !strings.Contains(bloco, "fim de release") {
		t.Fatalf("o bloco devia ser fechado: %q", bloco)
	}
	if !strings.Contains(bloco, "publique a tag") {
		t.Fatalf("o conteúdo devia estar no bloco: %q", bloco)
	}
}

// Habilidade inexistente é reportada, e as existentes seguem: derrubar a tarefa
// inteira por um nome trocado é pior do que seguir dizendo o que faltou.
func TestExpandReportsMissingWithoutDroppingTheRest(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Save("existe", "conteúdo real"); err != nil {
		t.Fatalf("Save falhou: %v", err)
	}
	bloco, missing := s.Expand([]string{"existe", "nao-existe"})
	if len(missing) != 1 || missing[0] != "nao-existe" {
		t.Fatalf("a habilidade faltante devia ser reportada: %v", missing)
	}
	if !strings.Contains(bloco, "conteúdo real") {
		t.Fatalf("a habilidade existente devia seguir no bloco: %q", bloco)
	}
}

// Sem nomes, não há bloco: uma tarefa sem "/" não pode ganhar texto extra.
func TestExpandWithoutNamesReturnsNothing(t *testing.T) {
	s, _ := newStore(t)
	bloco, missing := s.Expand(nil)
	if bloco != "" || len(missing) != 0 {
		t.Fatalf("esperava nada, veio %q / %v", bloco, missing)
	}
}

// Várias habilidades entram todas, na ordem pedida.
func TestExpandKeepsRequestedOrder(t *testing.T) {
	s, _ := newStore(t)
	for _, nome := range []string{"primeira", "segunda"} {
		if err := s.Save(nome, "corpo de "+nome); err != nil {
			t.Fatalf("Save falhou: %v", err)
		}
	}
	bloco, _ := s.Expand([]string{"segunda", "primeira"})
	iSegunda := strings.Index(bloco, "habilidade salva: segunda")
	iPrimeira := strings.Index(bloco, "habilidade salva: primeira")
	if iSegunda < 0 || iPrimeira < 0 {
		t.Fatalf("as duas deviam aparecer: %q", bloco)
	}
	if iSegunda > iPrimeira {
		t.Fatalf("a ordem pedida devia ser preservada: %q", bloco)
	}
}
