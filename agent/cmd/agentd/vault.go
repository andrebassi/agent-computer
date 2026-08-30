package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/tools"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/vault"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driving/api"
)

// Chaves do cofre. Nomes fixos porque são contrato entre o provisionamento e a
// subida do serviço — um nome digitado diferente nos dois lados produz "segredo
// ausente" com o segredo ali, gravado sob outro nome.
const (
	vaultKeyModelAPI = "agent/xai/apikey"
	vaultKeyHTTPAPI  = "agent/http/token"
)

// defaultVaultHome guarda a identidade age.
//
// Fica no disco do SISTEMA, e não em /workspace, de propósito: /workspace é o
// volume durável, fotografado por `task snapshot`, e a foto vai para a conta do
// DigitalOcean. Identidade junto do cofre na mesma partição tornaria a foto
// autossuficiente e o ganho da cifra seria zero.
const defaultVaultHome = "/etc/agentd/gopass"

// defaultPassphraseFile guarda a senha que abre a identidade.
//
// Arquivo, e não variável de ambiente da unidade: `systemctl cat` e
// /proc/<pid>/environ expõem o ambiente de um processo a quem puder lê-los.
const defaultPassphraseFile = "/etc/agentd/vault.pass"

// defaultToolUser é o usuário para quem as ferramentas do modelo caem.
//
// Precisa ser DIFERENTE de quem roda o agentd. Se forem o mesmo, o rebaixamento
// é nominal: o `bash -c` do modelo lê a identidade do cofre e a cifra em repouso
// deixa de proteger contra quem já está dentro da máquina.
const defaultToolUser = "agent"

// toolSandbox monta o rebaixamento das ferramentas.
//
// Desligado quando AGENTD_TOOL_USER vem vazio — o caso da máquina de
// desenvolvimento, onde não existem dois usuários. Em produção a unidade
// systemd define a variável.
//
// CLAUDE_CODE_OAUTH_TOKEN e ANTHROPIC_API_KEY atravessam o sudo porque são a
// credencial do PRÓPRIO agente de código: ela é feita para ser usada por ele, ao
// contrário da chave do modelo e do token da porta, que nunca saem do binário.
func toolSandbox() *tools.Sandbox {
	return tools.NewSandbox(envOr("AGENTD_TOOL_USER", defaultToolUser),
		"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "HOME")
}

// vaultConfig monta a configuração do cofre a partir do estado e do ambiente.
//
// O diretório do cofre desce de stateDir para o cofre acompanhar o volume nos
// verbos RESET e UPDATE, que preservam /workspace e descartam o resto.
func vaultConfig(stateDir string) (vault.Config, error) {
	home := envOr("AGENTD_VAULT_HOME", defaultVaultHome)
	passphraseFile := envOr("AGENTD_VAULT_PASSPHRASE_FILE", defaultPassphraseFile)
	passphrase, err := readPassphrase(passphraseFile)
	if err != nil {
		return vault.Config{}, err
	}
	return vault.Config{
		StoreDir:   stateDir + "/vault",
		HomeDir:    home,
		Passphrase: passphrase,
	}, nil
}

// envOr devolve a variável de ambiente ou o padrão.
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// readPassphrase lê a senha do cofre e RECUSA permissão frouxa.
//
// Mesma lógica do token da porta HTTP: a máquina é compartilhada por todos os
// agentes da conta, então "legível por outros" aqui quer dizer legível por
// qualquer processo — inclusive pelos que o modelo dispara.
func readPassphrase(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("senha do cofre ausente em %s — rode 'agentd -vault-init'", path)
		}
		return "", err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("senha do cofre em %s tem permissão %o — corrija com chmod 600", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// openVault abre o cofre, devolvendo nil quando ele não existe.
//
// Devolver nil em vez de erro é deliberado: quem chama passa o resultado para
// vault.Resolve, que cai para o ambiente. Uma máquina ainda não provisionada
// continua funcionando pela linha de comando — a migração não pode exigir um
// corte de serviço.
func openVault(ctx context.Context, stateDir string) *vault.Gopass {
	cfg, err := vaultConfig(stateDir)
	if err != nil {
		return nil
	}
	store, err := vault.OpenWith(ctx, cfg)
	if err != nil {
		return nil
	}
	return store
}

// resolveModelKey devolve a chave da xAI e de onde ela veio.
//
// Cofre primeiro, ambiente depois. A degradação é deliberada: o alvo desta
// mudança é o arquivo em claro no volume durável, que entra na foto do
// snapshot. Variável de ambiente de uma invocação por SSH morre com o processo
// e nunca teve esse problema — recusá-la quebraria todo uso por linha de
// comando sem melhorar segurança nenhuma.
func resolveModelKey(ctx context.Context, stateDir string) (string, string, error) {
	return vault.Resolve(ctx, openVault(ctx, stateDir), vaultKeyModelAPI, "XAI_API_KEY")
}

// sourceFile nomeia a origem "arquivo" do token da porta HTTP.
const sourceFile = "arquivo"

// readServeToken busca o token da porta HTTP no cofre e, na falta dele, no arquivo.
//
// A degradação é para o ARQUIVO, e não para o ambiente como na chave do modelo:
// é o arquivo que os scripts existentes leem, e trocar isso de uma vez deixaria
// a operação sem porta enquanto o cofre não estivesse provisionado em todas as
// máquinas.
//
// Falha FECHADA nos dois caminhos. Nenhum ramo aqui pode devolver token vazio
// com erro nil — seria porta aberta sem autenticação, e o teste que trava isso
// vale mais que a comodidade de um fallback silencioso.
func readServeToken(ctx context.Context, stateDir, tokenFile string) (string, string, error) {
	if store := openVault(ctx, stateDir); store != nil {
		value, err := store.Get(ctx, vaultKeyHTTPAPI)
		if err == nil && value != "" {
			return value, vault.SourceVault, nil
		}
	}
	token, err := api.ReadToken(tokenFile)
	if err != nil {
		return "", "", err
	}
	return token, sourceFile, nil
}

// runVaultInit cria o cofre e grava os segredos lidos da entrada padrão.
//
// A criação é em Go, e não em shell, de propósito: o mesmo binário que lê o
// cofre é o que o escreve, então o formato não tem como divergir entre os dois
// lados. Um provisionador escrito noutra linguagem produziria um arquivo que só
// falha na primeira leitura, longe da causa.
//
// Os valores entram pela ENTRADA PADRÃO, nunca por argumento: `ps` mostra a
// linha de comando de qualquer processo a qualquer usuário da máquina.
func runVaultInit(ctx context.Context, stateDir string, input io.Reader) error {
	passphraseFile := envOr("AGENTD_VAULT_PASSPHRASE_FILE", defaultPassphraseFile)
	if _, err := os.Stat(passphraseFile); err != nil {
		return fmt.Errorf("senha do cofre precisa existir antes em %s: %w", passphraseFile, err)
	}
	cfg, err := vaultConfig(stateDir)
	if err != nil {
		return err
	}
	if err := vault.Create(ctx, cfg); err != nil {
		// Cofre existente NÃO é erro aqui: reprovisionar é operação normal, e
		// abortar obrigaria a apagar o cofre para acrescentar um segredo — que é
		// como se perde tudo o que já estava lá.
		if !errors.Is(err, vault.ErrVaultExists) {
			return err
		}
		fmt.Fprintf(os.Stderr, "cofre já existe em %s, gravando nele\n", cfg.StoreDir)
	} else {
		fmt.Fprintf(os.Stderr, "cofre criado em %s (identidade em %s)\n", cfg.StoreDir, cfg.HomeDir)
	}
	written, err := loadSecretsFrom(ctx, cfg, input)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d segredo(s) gravado(s)\n", written)
	return nil
}

// loadSecretsFrom lê linhas "chave=valor" e as grava no cofre.
//
// Formato de linha, e não JSON, porque a entrada costuma vir de um `pass show`
// encadeado por SSH — e um parser de JSON no meio desse cano é uma peça a mais
// para falhar num lugar onde a falha é silenciosa.
func loadSecretsFrom(ctx context.Context, cfg vault.Config, input io.Reader) (int, error) {
	scanner := bufio.NewScanner(input)
	// Segredo pode ser longo: um JWT de serviço passa fácil dos 64 KB padrão do
	// bufio, e o erro que ele produz é "token too long", que não sugere nada.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	written := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return written, fmt.Errorf("linha sem '=': %q", firstChars(line))
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == "" {
			return written, fmt.Errorf("chave %s veio sem valor", key)
		}
		if err := vault.Put(ctx, cfg, key, value); err != nil {
			return written, err
		}
		// Só o NOME é impresso. O valor nunca vai para a saída, que costuma
		// terminar num log de deploy.
		fmt.Fprintf(os.Stderr, "  gravado: %s\n", key)
		written++
	}
	if err := scanner.Err(); err != nil {
		return written, fmt.Errorf("leitura da entrada falhou: %w", err)
	}
	return written, nil
}

// firstChars corta a linha para a mensagem de erro não carregar o segredo.
//
// Uma linha malformada ainda pode conter o valor inteiro; imprimi-la em erro
// venceria toda a proteção do cofre por causa de uma mensagem de diagnóstico.
func firstChars(line string) string {
	const limit = 12
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "…"
}
