# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## O que é

Lab que reproduz, em infraestrutura própria (DigitalOcean), a arquitetura do
[Grok Bot Computer](https://docs.x.ai/grok-bot/computer-and-apps): um desktop
virtual persistente onde agentes autônomos trabalham, com **take-over humano**
quando esbarram em senha, 2FA ou CAPTCHA.

São **três partes**, e quase todo trabalho toca só uma:

| Parte | Onde | O que é |
|---|---|---|
| o **computador** | `scripts/`, `cloud-init/`, `nixos/`, `Taskfile.yaml` | droplet descartável + volume durável, provisionado por cloud-init (Ubuntu) ou NixOS |
| o **agente** | `agent/` | binário Go `agentd`, hexagonal, que roda a tarefa numa tela |
| a **observação** | `probe/`, `observability/`, `flake.nix` | binário Go `agent-probe` (eBPF no kernel) + backend que roda **no Mac** |

⚠️ **`agent/` e `probe/` são MÓDULOS Go SEPARADOS**, com `go.mod` e gate de
cobertura próprios. A separação é decisão de segurança, não organização:
`cilium/ebpf` não pode virar dependência do binário que abre o cofre, e o
coletor precisa de privilégio enquanto o `agentd` foi desenhado para
**rebaixar**. `GOWORK=off` vale nos dois.

Documentação em pt-BR. **Identificadores em inglês, comentário e texto em pt-BR
acentuado** — é a convenção do repositório inteiro, e há scripts de renomeação
(`agent/scripts/rename-identifiers.py`, `scripts/rename-shell-vars.py`) porque
ela já foi violada em lote. Dívida conhecida e pequena, toda em teste: `caso`,
`casos`, `esperado`, `nome`, e as duas variáveis `AGENTD_TESTE_*` que só existem
dentro de `guardrails_test.go`. Não renomear de carona — sweep é decisão à parte.

## Git — repositório PRÓPRIO, não faz parte do `labs`

`git@gitlab.com:bassi-projects/agent-computer.git`, branch `main`. O diretório
está em `labs/.gitignore` (linha 143) de propósito: commitar no `labs` não traz
nada daqui, e nem cria gitlink. **Commit e push acontecem dentro deste
diretório.** `logs/`, `agent/cover.out` e `**/anthropic.env` são ignorados —
credencial vive no volume durável, nunca no repositório.

### Onde está escrito o quê — ler ANTES de reabrir uma decisão

`README.md` tem 3.544 linhas e é o documento principal (receituário, auditoria de
fidelidade à doc da x.ai, armadilhas já pagas, avaliação de KasmVNC e
CloakBrowser, pendências). Os sete arquivos de `docs/` respondem uma pergunta
cada:

| Arquivo | Responde |
|---|---|
| `TASK-LIFECYCLE.md` | por onde uma tarefa passa, estado a estado |
| `EXTENDING.md` | contrato campo a campo de conector, habilidade e runner |
| `STATE-FILES.md` | cada arquivo do volume: quem escreve, quem lê, se entra no prompt |
| `GUARDRAILS.md` | os detectores, os limiares e por que são ajustáveis |
| `SECURITY.md` | modelo de privilégio e os sete achados da revisão |
| `NOTIFICATIONS.md` | ligar ntfy e WebhookInbox do zero, e diagnosticar |
| `TEST-MAP.md` | que teste cobre que funcionalidade, e o que ele reprovaria |
| `OBSERVABILITY.md` | as três camadas de observação, o que cada uma responde e o que ela não responde |
| `KERNEL-VISIBILITY.md` | o que o kernel vê do que o modelo faz, e como sei que ele está vendo |

`examples/` traz o que já foi capturado rodando de verdade: quatro conectores
(`github.json`, `gitlab.yaml`, `cloudflare.yaml`, `digitalocean.yaml`) e três
habilidades (`web-search.md`, `web-diagnosis.md`, `change-review.md`). Copiar de
lá é mais barato que escrever do zero; `task examples` recaptura a saída real.

## Comandos

### Go (no Mac, não toca na máquina, não custa nada)

```bash
cd agent
GOWORK=off go test ./internal/...                              # suíte
GOWORK=off go test -run '^TestTaskLifecycleHappyPath$' ./internal/domain   # um teste só
./scripts/coverage-gate.sh                                     # o gate (task test:cov)
STRICT_PACKAGES=1 ./scripts/coverage-gate.sh                   # reprova pacote abaixo de 90%
MINIMO=101 ./scripts/coverage-gate.sh                          # prova de falha: tem que sair rc≠0
```

🛑 **`GOWORK=off` é obrigatório.** O `~/works/go.work` do workspace **não lista
este módulo**, e sem a variável o build falha com *"directory prefix
internal/domain does not contain modules listed in go.work"* — erro que não tem
nada a ver com o código. O gate já a exporta; comando manual não.

O gate reprova em três frentes: total < 90%, domínio < 100%, e testes com `-race`
falhando. Cobertura é de **statement** (Go não mede branch); a compensação
declarada é tabela cobrindo cada ramo de decisão. Estado medido em 31/08/2026:
108 arquivos Go, 51 deles de teste, 511 funções de teste, **90,2% total e domínio
100%**.

Cobertura **por pacote** só avisa por padrão; `STRICT_PACKAGES=1` reprova. Ligar
o estrito hoje deixaria o gate permanentemente vermelho, e gate sempre vermelho é
gate desligado — são **8 pacotes** abaixo, o pior `journal` em 81,8%, depois
`tools` 85,1%, `api` 86,0%, `pricing` 86,5%, `vault` 88,6%, `runners` 89,4%,
`lock` 89,5%, `store` 89,8%. Uma exclusão declarada: `secret` (31,2%), cujo
caminho principal exige um TTY de verdade.

### Task (na raiz; o que toca a máquina gasta dinheiro)

```bash
task -l               # lista as 48 tasks

# só no Mac, de graça
task lint             # gate dos scripts shell (shellcheck + variável órfã + load_token)
task nixos:validate   # avalia nixos/host.nix
task probe:cov        # gate de cobertura do coletor eBPF (módulo probe/)
task test:cov         # o gate de cobertura (roda em agent/)

# ciclo de vida da máquina
task check            # binários, token, chave, latência — ANTES de gastar
task up               # volume + droplet + espera + valida
AGENT_OS=nixos task up  # o mesmo, porém NixOS declarativo via nixos-infect
task status | health | cost | pending
task open             # túnel SSH para o noVNC
task ssh              # entra como `agent` (as permissões do modelo)
task screens          # cria mais telas na MESMA máquina
task update           # rebuild preservando /workspace  → exige `task deploy` depois
task reset            # volta ao snapshot
task snapshot | restore | image-snapshot | destroy

# agente
task deploy           # compila e instala o agentd por SSH de root (o gate roda antes)
task vault            # provisiona o cofre cifrado com os segredos do pass
task catalog          # lista conectores e habilidades instalados
task serve-enable     # sobe a porta HTTP e o timer de avisos, ligados no boot
task serve-status | serve-logs | restart | logs
task notify-setup     # liga o destino dos avisos (ntfy)
task examples         # recaptura examples/ com saída real

# observação — o backend roda NO MAC (Nix, não Docker)
task obs:up           # Grafana + VictoriaTraces + VictoriaLogs + VictoriaMetrics
task obs:status       # pergunta a cada porta se ela responde
task obs:open         # abre o Grafana (painel `agent-computer` já provisionado)

# o coletor eBPF (módulo probe/)
task ebpf:gate        # mede se a máquina suporta: kernel, BTF, tracefs, os hooks
task ebpf:gate:proof  # prova de falha do gate, nos dois sentidos
task probe:deploy     # compila os objetos BPF e instala o coletor por SSH de root
task probe:test       # prova que ele vê execve E conexão, com prova de falha
task probe:run        # roda na máquina em primeiro plano, imprimindo tudo

# verificação
task suites           # as 6 suítes de máquina, em ordem, para na primeira falha
task functional       # 3 testes que CHAMAM O MODELO (custa token)
task hostile          # entrada malformada, degradação, concorrência
task guardrails-test | redaction-test | ssrf-test | privilege-test | serve-test | e2e
task delegation-test | web-search-test | persist-test | integration-test | probe-web
```

`task suites` roda, nesta ordem: `08-validate` → `27-privilege-test` →
`25-serve-integration-test` → `32-end-to-end` → `35-connector-ssrf-test` →
`36-guardrails-test`. A ordem não é alfabética por acaso — validar antes de
provar contenção evita atribuir a um guardrail a falha de uma máquina meio
provisionada.

Toda task registra em `logs/<nome>.log` (diretório gitignored, recriado por
`mkdir -p` em cada task). O `set: [pipefail]` no topo do Taskfile não é enfeite:
sem ele `cmd | tee` devolve o rc do `tee` e **toda task passa verde** — o
primeiro `task up` saiu rc=0 sem ter criado droplet nenhum.

### Dentro da máquina

```bash
agentd -screen 1 -prompt "a tarefa"          # roda
agentd -resume  -task <id> -note "resolvi"   # devolve o controle após take-over
agentd -abandon -task <id>                   # desiste e libera a tela
agentd -catalog list                         # conectores e habilidades instalados
agentd -serve -listen 127.0.0.1:8787         # porta HTTP
agentd -notify-drain -webhook <url>          # entrega a fila de avisos (roda por timer)
agentd -vault-init | -vault-check            # cria o cofre / prova que ele ABRE aqui
agentd -connector-probe <url>                # alcança essa URL como um conector alcançaria
```

Flags menos citadas, mas que mudam comportamento, com o padrão medido:
`-state` (`/workspace/agent`, o estado durável), `-model` (vazio = `grok-4.6`),
`-task-timeout` (`2h`, teto de **relógio**, distinto do teto de turnos),
`-token-file` (`<state>/api-token`; o token vem de arquivo, nunca de argumento —
`ps` o exporia) e `-listen` (`127.0.0.1:8787` — use o IP da malha, **nunca**
`0.0.0.0`).

`@nome` anexa um conector à tarefa, `/nome` injeta uma habilidade. Só o conector
**anexado** vira ferramenta.

### O coletor eBPF (`probe/`, binário `agent-probe`)

```bash
agent-probe -verbose                              # imprime cada execve e conexão
agent-probe -sink-file /etc/agent-probe/sink.url  # entrega ao backend; é o que a unit usa
```

Duas probes de kernel — `sched/sched_process_exec` (o que o modelo executou) e
`sock/inet_sock_set_state` (para onde ele discou, com destino privado marcado) —
mais amostragem de PSI, que **não é eBPF e é honesto dizer**.

O nome é `agent-probe` e **nunca** `agentd-probe`: o sudoers dá ao usuário
`agent` — logo ao modelo — `pkill -9 -f agentd*`, que casaria o segundo nome.

### Telemetria: onde ligar

```bash
AGENTD_OTLP_ENDPOINT=127.0.0.1:4317          # trechos  -> VictoriaTraces (gRPC)
AGENTD_OTLP_METRICS_ENDPOINT=127.0.0.1:8428  # métricas -> VictoriaMetrics (HTTP)
AGENTD_LOG_LEVEL=debug                       # nível do log JSON
/etc/agent-probe/sink.url                    # destino dos eventos de kernel
```

⚠️ São **backends diferentes**. Apontar os dois para a mesma porta produz
`rpc error: Unimplemented — MetricsService/Export`, que parece erro de rede e não
é: o VictoriaTraces só implementa o serviço de traces.

## Arquitetura

### O computador: o droplet é descartável, o volume é o computador

```
volume 20 GB (agent-computer-workspace)      droplet s-2vcpu-4gb (substituível)
        └── /workspace                              ├── /scratch    efêmero
             ├── browser/screen-N   perfil Chrome   ├── pacotes     efêmero
             ├── projects/                          └── sistema     efêmero
             └── agent/             estado do agentd
```

Sem essa separação o verbo **Update** da doc é impossível. `task update`
reconstrói preservando `/workspace`; `task reset` volta ao snapshot; `Recover`
não tem comando próprio porque é o mesmo que `update`.

**Uma máquina, N telas** — units systemd são templates, `screen-add N` cria a
tela N (VNC 5900+N, noVNC 6080+N, CDP 9220+N). Telas **não são fronteira de
segurança**: compartilham `/workspace`, credenciais e sudo. ~500 MB de RAM cada.

Nada é publicado: `ufw` só deixa a 22, VNC/noVNC/CDP escutam em loopback, o
acesso é túnel SSH (`task open`) ou a malha Tailscale — os scripts **preferem a
malha** porque o IP público muda a cada rebuild.

**Dois sistemas, escolhidos por `AGENT_OS`**: `ubuntu` (padrão, cloud-init de 667
linhas, o caminho verificado) e `nixos` (declarativo, `nixos/host.nix`, instalado
por `nixos-infect` sobre o Ubuntu — não existe imagem NixOS no DigitalOcean).

### O agente: hexagonal, `agent/internal/`

```
domain/    Task, Conversation, Connector, SecretRequest, Event — ZERO imports
           externos, 100% de cobertura
service/   Agent (o laço), Lifecycle, guardrails, toolrun
ports/     LanguageModel, Tool, ScreenDriver, TaskStore, ScreenLock, EventSink,
           SecretStore, SecretPrompter, TaskRunner (único porto de entrada)
secretref/ ordem única de busca de credencial: cofre primeiro, arquivo depois
adapters/driven/    xai, tools, browser (CDP), screen, store, lock, journal,
                    connectors, vault (gopass+age), events, pricing, skills,
                    runners, secret, telemetry (OTel: trecho e métrica)
adapters/driving/   api (porta HTTP + Supervisor)
cmd/agentd/         único lugar que conhece implementações concretas
```

Direção: **adapters → ports → domain**. `domain` não importa nem `ports` — por
isso o laço mora em `service`.

**O laço** (`service/agent.go`): trava a tela → `task.Start()` → até 60 iterações
de `modelo.Complete` → executa ferramentas → sem chamadas, `Finish()`. Três
decisões embutidas: falha de ferramenta vira texto no histórico e **não** derruba
a tarefa (`grep` sem match já devolve 1); teto de 60 iterações; e `BlockRequest`
**para o laço na hora** — continuar seria "tentar contornar a verificação", que é
o que a doc manda não fazer.

**Ferramentas**: `shell`, `request_takeover`, `browser_*` (6, falam CDP direto
com o Chrome da tela), `delegate_to_code` (entrega código a Claude Code/opencode)
e `<conector>.<operação>`. `ToolSpec.Concurrent` é falso por padrão, e isso é
decisão de segurança: as ferramentas disputam a mesma aba e o mesmo `/workspace`.

**Porta HTTP** (`adapters/driving/api`): `GET /health` sem auth, e
`POST /tasks`, `GET /tasks/{id}`, `POST /tasks/{id}/resume|abandon` com
`Authorization: Bearer`. O `Supervisor` reconcilia tarefas órfãs no boot e impõe
o teto global de tarefas simultâneas.

**Notificação: enfileirar e entregar são processos diferentes** (`driven/events`).
`Publish` é **escrita local** no volume — não pode falhar por serviço remoto fora
do ar, e por isso pode ser chamada de dentro da tarefa. Quem entrega é o `Drain`,
noutro processo, chamado por timer (`agentd -notify-drain`). É essa separação que
satisfaz o requisito duro: **matar a sessão que iniciou a tarefa não mata a
entrega**, porque a entrega nunca esteve nela.

A fila só é limpa quando **tudo** saiu — entrega parcial que limpasse perderia o
aviso mais recente, que é o mais urgente. O preço aceito é o oposto: um aviso pode
sair duas vezes. Aviso repetido incomoda; aviso perdido deixa uma tela travada sem
ninguém saber.

Dois destinos ao mesmo tempo, formatos diferentes: **ntfy** (texto, para agir do
celular) e **WebhookInbox** (JSON cru com cabeçalhos, para depurar — expira em 1 h).
Passo a passo em `docs/NOTIFICATIONS.md`; `task notify-test` prova ponta a ponta.

### Três pontos de extensão — escolher o errado custa retrabalho

| Quer que o agente… | Use | Vive em |
|---|---|---|
| chame uma API com credencial que ele não pode ver | **conector** (manifesto YAML/JSON) | `/workspace/agent/connectors/` |
| siga um procedimento repetido | **habilidade** (markdown, entra no prompt) | `/workspace/agent/skills/` |
| use outro agente de código | **runner** | `/workspace/agent/runners.json` |

Contrato campo a campo em `docs/EXTENDING.md`; o que cada arquivo de estado é,
quem o escreve e se ele entra no prompt, em `docs/STATE-FILES.md`; modelos
prontos em `examples/`. Erro comum: habilidade que ensina a chamar API com
`curl` — a credencial passa a viver na linha de comando, ao alcance do modelo.

### Guardrails (`service/guardrails.go`)

Detectar em código Go com limiar numérico, conter por `task.Block` (a mesma
máquina do take-over), lembrar em quatro arquivos no volume. Os tetos são
ajustáveis por ambiente **porque teto que só muda recompilando é teto
desligado** — e porque o teste não consegue forçá-lo pedindo ao modelo que
insista (medido: ele repetiu 2 vezes e parou, com o limiar em 3).

| Variável | Padrão | Para quê |
|---|---|---|
| `AGENTD_MAX_TURNS` | 180 | turnos acumulados, atravessando retomadas |
| `AGENTD_MAX_TOOL_FAILURES` | 3 | falhas idênticas seguidas = laço |
| `AGENTD_MAX_COST_USD` | 3.00 | teto de inferência por tarefa |
| `AGENTD_MAX_CONCURRENT_TASKS` | — | teto global de tarefas simultâneas |

`BlockGuardrail` é o sexto motivo de bloqueio e é **nosso** — os cinco da doc
descrevem o que o *site* exige; reaproveitá-los faria a tela mentir sobre a causa.

Três outras variáveis mudam contenção e cofre, e não são de teste:

| Variável | Padrão | Efeito |
|---|---|---|
| `AGENTD_TOOL_USER` | `agent` | usuário do sandbox das ferramentas. **Vazia desliga o sandbox** — só o caso do Mac, nunca a máquina |
| `AGENTD_VAULT_HOME` | `/etc/agentd/gopass` | onde vive a identidade age do cofre |
| `AGENTD_VAULT_PASSPHRASE_FILE` | `/etc/agentd/vault.pass` | senha que abre a identidade; nunca em argumento |

## Modelo de privilégio — o adversário é o próprio modelo

A máquina só escuta em loopback; a pergunta de segurança é o que o modelo alcança
a partir da ferramenta de shell (que é a função do produto, não um defeito).

| Usuário | Quem é | Alcança |
|---|---|---|
| `agentd` | o serviço | o cofre, `/etc/agentd/` |
| `agent` | as ferramentas do modelo | `/workspace`, sudo por lista fechada |
| `root` | o **operador**, por chave SSH que só existe no Mac | tudo |

Três coisas ficam fora do alcance do modelo: credenciais que não são dele, as
próprias regras (habilidade é instrução, conector é alcance de rede) e o binário
do serviço — que por isso mora em `/usr/local/bin/agentd` `root:root 0755` e é
instalado **por SSH de root**. Consequência: **todo `task update` pede `task
deploy` depois**.

Nos scripts (`scripts/lib.sh`), isso vira três funções distintas — usar a errada
desfaz a contenção:

| Função | Entra como | Para quê |
|---|---|---|
| `agent_ssh` | `agent` | diagnóstico — exercita as permissões de verdade |
| `root_ssh` | `root` | deploy, catálogo, cofre |
| `agentd_run` | root → `setpriv` para `agentd` | rodar o `agentd` (dono do cofre) |

`setpriv` em vez de `sudo` de propósito: uma linha a menos no sudoers é uma linha
a menos que o modelo poderia herdar. Detalhe e os sete achados da revisão em
`docs/SECURITY.md`.

## O transporte da telemetria

A máquina **empurra**; nada nela escuta. É o que preserva o invariante que
`08-validate.sh` testa: toda porta em `127.0.0.1`, e só a 22 no firewall.

```bash
# túnel reverso — o caminho que funciona hoje
ssh -N -f -R 4317:127.0.0.1:4317 -R 8428:127.0.0.1:8428 -R 9428:127.0.0.1:9428 root@<ip>
```

⚠️ O Tailscale da máquina está **`Logged out`** (medido em 31/08/2026): o
`tailscaled` roda, mas o nó não está autenticado. Religá-lo exige `tailscale up`,
que é interativo por decisão do repositório. Até lá, o túnel é o caminho.

⚠️ Expor as portas do backend no IP público **não** é alternativa: elas são de
laboratório em loopback e não têm autenticação.

## Convenções e armadilhas do repositório

- **`scripts/NN-nome.sh` numerados por ordem de uso** (00 pré-requisitos → 42
  notificação ponta a ponta; o 28 não existe), todos sourceando `scripts/lib.sh`
  e chamando `load_token` antes de qualquer `doctl`/`agent_ssh`. O `task lint`
  reprova as duas omissões que já passaram despercebidas: variável órfã (sobra de
  renomeação, que fez um teste passar verde imprimindo vazio) e `agent_ssh` sem
  `load_token`.
- **`lib.sh` não tem `set -e`** — é sourceado, e o flag vazaria para o chamador,
  matando-o no primeiro `grep` sem resultado, sem mensagem.
- **`cloud-init/user-data.yaml` é ASCII puro**, e `01-create.sh` reprova qualquer
  byte não-ASCII antes de criar. O DigitalOcean duplo-codifica UTF-8 no caminho
  API → ConfigDrive, o cloud-init recusa o arquivo inteiro **em silêncio**, e o
  droplet sobe reportando `done` sem ter instalado nada. Custou três droplets.
  Os scripts locais levam acento normalmente.
- **`scripts/suite-lock.sh`** impede duas suítes contra a mesma máquina — log
  entrelaçado já produziu um `erros: 0` sem valor e um `erros: 1` mentiroso.
- **Suíte de máquina roda duas vezes seguidas** para valer: teste de contenção
  deixa estado atrás justamente quando funciona (tarefa bloqueada trava a tela, e
  o estado é durável — sobrevive a reboot, destroy e rebuild; `-abandon` é a
  saída).
- **Um teste só cobre uma funcionalidade se falhar quando ela for removida.**
  `docs/TEST-MAP.md` aplica isso item a item — foi assim que `claude --version`
  deixou de passar por prova de que a delegação funciona.
- **`task pending`** levanta o que falta perguntando à máquina, não à memória.
- **O clang da Apple NÃO gera objeto BPF**, e o *wrapper* do nixpkgs passa no
  `-print-targets` mas falha ao compilar (injeta `-mmacos-version-min`). Por isso
  `45-deploy-probe.sh` detecta o compilador **compilando um objeto de teste**, em
  vez de perguntar. E usa `find -L`: no nixpkgs `bin/clang` é symlink.
- **Contrato de ABI se confere no `format` do kernel**, nunca de memória nem de
  blog. Um teste unitário escrito pela mesma cabeça que escreveu o decodificador
  herda a suposição dela: foi assim que a porta 443 saiu como 47873 e o teste
  passou. Quem pegou foi rodar na máquina.
- **No VictoriaMetrics o nome da métrica preserva os pontos** e não ganha
  `_total`. `agentd_model_tokens_total` devolve vazio **sem erro** — o painel
  parece sem dado em vez de com consulta errada. Use `{__name__="agentd.model.tokens"}`.
