package tools

import (
	"context"
	"os/exec"
	"os/user"
	"strings"
)

// Sandbox rebaixa o subprocesso para um usuário sem acesso ao cofre.
//
// # Por que existe
//
// O modelo dirige a ferramenta de shell. Sem rebaixamento, o `bash -c` dele roda
// com o MESMO usuário do agentd — e o agentd é dono da identidade age que abre o
// cofre. Um `cat` no arquivo de senha entregaria todos os segredos, e a cifra em
// repouso viraria enfeite: ela protegeria a foto do volume e não protegeria nada
// contra quem está dentro da máquina.
//
// A separação é o mecanismo inteiro:
//
//	agentd   dono da identidade e da senha do cofre   NÃO executa nada do modelo
//	agent    dono do navegador e de /workspace        executa TUDO do modelo
//
// # Por que sudo, e não setuid direto
//
// Trocar de usuário com SysProcAttr.Credential exige privilégio que o agentd não
// tem — e não deve ter, porque rodar o serviço como root para depois rebaixar
// põe root no caminho de todo defeito do binário. Com sudo, a transição é UMA
// linha declarada em sudoers, legível e auditável, e o agentd continua sem
// nenhum privilégio próprio.
//
// A direção é sempre de redução: agentd (com cofre) para agent (sem cofre).
// Nunca o contrário.
type Sandbox struct {
	// user é para quem o subprocesso cai. Vazio desliga o rebaixamento, que é o
	// caso da máquina de desenvolvimento e dos testes.
	user string
	// preserved lista as variáveis que atravessam o sudo.
	//
	// Precisa ser explícito: sudo limpa o ambiente por padrão, e é isso que se
	// quer — só o que foi nomeado passa. As variáveis vão por ambiente, nunca
	// por argumento, porque `ps` mostra a linha de comando de qualquer processo
	// a qualquer usuário da máquina.
	preserved []string
}

// NewSandbox monta o rebaixamento para o usuário informado.
//
// Usuário vazio devolve um sandbox inerte: o comando roda como está. É o que
// vale em teste e na máquina de desenvolvimento, onde não há dois usuários.
func NewSandbox(user string, preserved ...string) *Sandbox {
	return &Sandbox{user: strings.TrimSpace(user), preserved: preserved}
}

// Enabled diz se o rebaixamento está de fato ligado.
//
// Desliga sozinho quando o processo JÁ é o usuário de destino. Isso acontece de
// verdade: os scripts de operação rodam o agentd por SSH como `agent`, e ali um
// `sudo -u agent` exigiria uma concessão de sudoers que não existe — o comando
// falharia com "not allowed to execute", mensagem que manda procurar no lugar
// errado. Não há perda de segurança: rebaixar para si mesmo não rebaixa nada.
func (s *Sandbox) Enabled() bool {
	if s == nil || s.user == "" || s.user == disableToken {
		return false
	}
	return s.user != currentUserName()
}

// disableToken desliga o rebaixamento de forma explícita.
//
// Existe porque uma variável VAZIA não serve de desligamento: ela é
// indistinguível de "esqueceram de definir", e nesse caso o padrão precisa ser
// o seguro. Desligar tem de ser uma decisão escrita.
const disableToken = "off"

// currentUserName devolve o nome do usuário do processo.
//
// Erro devolve string vazia, que nunca casa com um usuário de destino real — o
// desfecho é rebaixar, que é o lado seguro de errar.
func currentUserName() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return current.Username
}

// Command monta o comando já rebaixado.
//
// `-n` recusa qualquer prompt: um sudo que pedisse senha penduraria a ferramenta
// até o tempo limite, e o sintoma seria "comando excedeu 120s" — que manda
// procurar no lugar errado.
//
// `--` fecha as opções do sudo. Sem isso, um comando do modelo começando com
// hífen seria interpretado como opção do próprio sudo.
// CommandPreserving é o Command com variáveis EXTRAS atravessando o sudo.
//
// Existe para o runner alternativo: cada agente de código tem a sua credencial
// (`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …), e `sudo --preserve-env` só deixa
// passar o que está listado. Fixar todos os nomes aqui acoplaria o sandbox ao
// catálogo — e cada runner novo exigiria editar este arquivo.
//
// Quem chama já leu o arquivo de credencial e sabe exatamente quais variáveis
// aquele runner define. Preservar só essas é o mínimo necessário: a credencial
// de um runner não atravessa para outro.
func (s *Sandbox) CommandPreserving(ctx context.Context, extra []string, name string, args ...string) *exec.Cmd {
	if !s.Enabled() || len(extra) == 0 {
		return s.Command(ctx, name, args...)
	}
	widened := &Sandbox{user: s.user, preserved: append(append([]string{}, s.preserved...), extra...)}
	return widened.Command(ctx, name, args...)
}

func (s *Sandbox) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if !s.Enabled() {
		return exec.CommandContext(ctx, name, args...)
	}
	prefix := []string{"-n", "-u", s.user}
	if len(s.preserved) > 0 {
		prefix = append(prefix, "--preserve-env="+strings.Join(s.preserved, ","))
	}
	prefix = append(prefix, "--", name)
	return exec.CommandContext(ctx, "sudo", append(prefix, args...)...)
}

// Describe resume o rebaixamento para o log de subida.
//
// Vai para o log de propósito: uma máquina que subiu sem rebaixamento parece
// idêntica a uma rebaixada até alguém tentar ler o cofre pelo shell do modelo —
// e aí é tarde.
func (s *Sandbox) Describe() string {
	if !s.Enabled() {
		return "sem rebaixamento (as ferramentas rodam como o próprio agentd)"
	}
	return "ferramentas rebaixadas para o usuário " + s.user
}
