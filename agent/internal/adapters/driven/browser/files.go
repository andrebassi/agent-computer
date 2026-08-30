package browser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// jsonUnmarshal existe para o pacote não importar encoding/json em dois lugares
// só por causa de uma chamada.
func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

// writeBase64 decodifica e grava uma captura de tela.
//
// O diretório é criado se faltar: a captura costuma ser pedida no meio de uma
// tarefa que deu errado, e falhar por diretório ausente esconderia o problema
// original atrás de um erro de escrita.
func writeBase64(path, encoded string) error {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("imagem inválida: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("criando diretório da captura: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
