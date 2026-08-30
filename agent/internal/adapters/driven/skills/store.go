// Package skills guarda habilidades salvas: instruções reutilizáveis que a
// pessoa referencia com "/" no texto da tarefa.
//
// É o análogo do que a documentação chama de saved skill. A ideia é evitar
// reescrever o mesmo procedimento longo a cada tarefa — em vez de colar dez
// linhas explicando como publicar um release, escreve-se "/release".
//
// Mora em /workspace, no volume durável: uma habilidade escrita hoje precisa
// sobreviver à reconstrução do computador, junto com o resto do estado.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// namePattern limita o nome ao que é seguro em nome de arquivo.
//
// Sem esta trava, um nome com sequências de subida de diretório faria a leitura
// escapar da pasta. É a mesma classe de defeito que filepath.Base evita no
// registro de conectores, e vale em dobro aqui: o nome vem do texto que a pessoa
// digitou, depois de passar pelo parser de marcadores.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

// maxSkillBytes limita o tamanho de uma habilidade.
//
// O conteúdo é injetado no prompt, e prompt gigante custa token a cada iteração
// da tarefa — não uma vez. 8 KB comportam um procedimento detalhado sem virar
// um manual.
const maxSkillBytes = 8000

// Store lê e grava habilidades em disco.
type Store struct {
	dir string
}

// NewStore cria o diretório de habilidades dentro do estado durável.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório de habilidades: %w", err)
	}
	return &Store{dir: dir}, nil
}

// path monta o caminho de uma habilidade, validando o nome antes.
func (s *Store) path(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("nome de habilidade inválido: %q (use letras, números, hífen e sublinhado)", name)
	}
	return filepath.Join(s.dir, name+".md"), nil
}

// Get lê uma habilidade.
func (s *Store) Get(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("habilidade %q não existe", name)
		}
		return "", err
	}
	return string(data), nil
}

// Save grava uma habilidade, recusando conteúdo vazio ou grande demais.
func (s *Store) Save(name, content string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("habilidade %q vazia", name)
	}
	if len(content) > maxSkillBytes {
		return fmt.Errorf("habilidade %q tem %d bytes; o limite é %d, porque o conteúdo entra no prompt a cada iteração",
			name, len(content), maxSkillBytes)
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// Remove apaga uma habilidade.
func (s *Store) Remove(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("habilidade %q não existe", name)
		}
		return err
	}
	return nil
}

// List devolve os nomes das habilidades salvas, em ordem alfabética.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}

// Expand monta o bloco de habilidades para juntar ao prompt da tarefa.
//
// Habilidade inexistente vira aviso em vez de erro fatal: a pessoa pode ter
// digitado errado, e derrubar a tarefa inteira por um nome trocado é pior do que
// seguir e dizer o que faltou.
//
// O bloco vem delimitado e nomeado para o modelo distinguir a instrução salva do
// pedido em si. Sem a delimitação, um procedimento longo se mistura à tarefa e o
// modelo passa a tratar o procedimento como o objetivo.
func (s *Store) Expand(names []string) (string, []string) {
	if len(names) == 0 {
		return "", nil
	}
	var b strings.Builder
	missing := []string{}
	for _, name := range names {
		content, err := s.Get(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		fmt.Fprintf(&b, "\n\n--- habilidade salva: %s ---\n%s\n--- fim de %s ---",
			name, strings.TrimSpace(content), name)
	}
	return b.String(), missing
}
