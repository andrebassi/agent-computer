# Guardrails do laço

Contenção do **comportamento do agente em execução** — separada da contenção de
infraestrutura (cofre, rebaixamento de usuário, firewall, discador), que vive em
[`SECURITY.md`](SECURITY.md).

A ideia dos quatro arquivos vem do [ralph](https://github.com/iannuttall/ralph).
O mecanismo, não.

## A fronteira: o que é código e o que é texto

É a única pergunta que importa numa camada de guardrails, e a resposta do ralph
é desconfortável. Lá, **o único gate determinístico do sistema inteiro** é um
`grep` procurando `<promise>COMPLETE</promise>` no stdout do agente — um sinal
que o próprio modelo decide emitir.

| O ralph promete | O código do ralph faz |
|---|---|
| *"Signs are injected into context at the start of each iteration"* | injeta o **caminho** do arquivo e pede que o modelo o leia. Nenhuma linha lê o conteúdo |
| quality gates do PRD | viram bullets de texto no prompt; o loop não executa comando nenhum |
| detecção automática de thrashing | documentação de um ancestral — não existe |
| recuperação de crash (`STALE_SECONDS`) | vem `0` de fábrica: desligada |

Aqui a divisão é explícita:

| Camada | Mecanismo |
|---|---|
| **detectar** | código Go no laço, com limiar numérico |
| **conter** | `task.Block`, a mesma máquina do take-over — o estado muda, não uma frase |
| **lembrar** | quatro arquivos no volume, escritos pelo serviço |
| **realimentar** | o serviço **lê** e concatena ao prompt de sistema |

Nada da contenção depende de o modelo cooperar.

## Os quatro arquivos

Em `/workspace/agent/`, todos `agentd:agent 0640`.

| Arquivo | Papel |
|---|---|
| `guardrails.md` | lições que **entram no prompt de sistema** de toda tarefa nova |
| `progress.md` | desfecho de cada tarefa, append-only |
| `activity.log` | uma linha por iteração: ferramenta, duração, tokens, motivo de parada |
| `errors.log` | falha de ferramenta com a contagem de repetição |

**O modelo lê e nunca escreve.** O grupo `agent` tem leitura para o operador
conferir sem virar root; escrita, não. É a mesma razão de `skills/` ser do
`agentd`: conteúdo que entra no prompt é instrução, e quem controla a própria
instrução não está contido. No ralph o agente escreve as próprias Signs — aqui
isso seria o caminho para neutralizar o guardrail que o incomoda.

O `guardrails.md` tem duas propriedades que um `append` não dá: **lição repetida
não duplica** (o mesmo detector dispara de novo semanas depois) e **teto de
bytes com descarte do mais antigo** (o arquivo entra no prompt inteiro, em toda
iteração de toda tarefa).

## Os três detectores

Rodam **antes** de chamar o modelo — parar depois de gastar o turno seria pagar
o que o teto existe para evitar. Todos terminam em `blocked` + take-over.

| Detector | Limiar | Ajustável por |
|---|---|---|
| turnos acumulados por tarefa | 180 | `AGENTD_MAX_TURNS` |
| mesma ferramenta falhando com os mesmos argumentos | 3 | `AGENTD_MAX_TOOL_FAILURES` |
| fração do tempo da tarefa | 80% de 2 h | — |

Mais um caso que não é limiar: **resposta truncada**. Um `finish_reason:
"length"` chega sem chamada de ferramenta, e o laço tratava isso como conclusão
— a tarefa terminava `done`, com a resposta pela metade e ninguém sabendo.

### Por que `blocked` e não `failed`

O guardrail **para** a tarefa; não a joga fora. O trabalho, o histórico e a tela
ficam reservados, e a pessoa decide se retoma. Se ele encerrasse, parar cedo
custaria tudo o que já tinha sido feito — e a primeira coisa que alguém faria é
desligá-lo.

O motivo de bloqueio é o sexto (`guardrail`), separado dos cinco da documentação
do produto de propósito: aqueles descrevem o que o **site** exige; este é nós
parando o agente. Reaproveitar `human_required` faria a tela dizer "o site exige
uma pessoa" quando o site não exigiu nada.

## Multi-runner na delegação

`delegate_to_code` aceita um `runner` opcional, resolvido contra
`/workspace/agent/runners.json` (`agentd:agent 0640`). Sem ele, Claude Code como
sempre.

O ralph faz o mesmo com `AGENT_CMD` — e com `eval` sobre uma string de shell.
Aqui o comando é **vetor**, vai direto para `exec.Command`, e o catálogo recusa
qualquer entrada cujo binário seja um interpretador (`sh`, `bash`, `env`,
`xargs`). O motivo é concreto: com shell no meio voltam `;`, `&&` e `$(...)`, e
com eles `sudo` — que desfaz o rebaixamento do modelo para o usuário `agent`.

Cadastrados: `claude` (instalado), `codex`, `droid`, `opencode`, `kiro`. Os
quatro últimos ficam no catálogo antes de existirem na máquina de propósito —
pedir um deles falha **nomeando o binário que falta**, o que é melhor
documentação de instalação que uma lista em outro arquivo.

O prompt viaja em **arquivo** 0600, não em argumento: argumento é visível em
`ps` para qualquer usuário da máquina, inclusive o do modelo.

## Consertos que vieram junto

Os detectores exigiram fechar quatro buracos que já existiam:

| Buraco | Consequência |
|---|---|
| `ToolResult.Failed` escrito por toda ferramenta, **lido por ninguém** | comando de shell que falhava entrava no histórico pelo ramo de sucesso; não havia dado para detectar repetição |
| contador de iterações zerava a cada `Resume` | tarefa que alternasse bloqueio e retomada ganhava 60 turnos novos a cada volta, sem teto sobre o total |
| `StopReason` preenchido e nunca lido | resposta truncada virava `done` com sucesso |
| `Resume` persistia `running` **antes** de tomar a trava | tarefa `blocked` virava `failed` quando a tela estava ocupada — o trabalho e o pedido de ajuda iam junto |

Os campos de token também passaram a ser lidos, e vão para o `activity.log`.
Isto **ainda não é teto de custo** — é o registro que torna um teto possível
depois, com número medido em vez de estimado.

## Verificação

```bash
task test:cov          # ≥90% total, domínio 100%, com -race
task guardrails-test   # os detectores na máquina real
```

`scripts/36-guardrails-test.sh` tem 11 seções, e duas merecem menção:

- **seção 6** prova que a lição gravada **chega ao prompt de sistema** da tarefa
  seguinte. É o que separa este trabalho do ralph, onde a lição fica num arquivo
  que ninguém lê;
- **seção 8b** força o limiar para **1** pelo ambiente e prova que o detector
  **bloqueia de verdade**, com a tarefa criada por ela mesma.

  O limiar forçado não é para facilitar: é a única forma determinística de
  exercitar o caminho. Medido em 30/08/2026, na mesma tarefa e com a mesma
  instrução explícita para insistir, o modelo repetiu **duas** vezes numa rodada
  e desistiu na **primeira** na seguinte. Com limiar 2 o teste passava e
  reprovava alternadamente sem nada mudar no produto — a pior espécie de teste,
  porque ensina a ignorar o vermelho.

  A seção também **libera a tela 4 antes de começar**: a execução anterior deixa
  ali justamente uma tarefa bloqueada, e sem limpar a segunda rodada não cria
  tarefa nenhuma. Teste que só passa uma vez não é teste.

A seção 8 é a outra direção: tarefa normal **não** dispara detector nenhum. Sem
ela, um detector quebrado que bloqueasse tudo passaria em todas as demais.

## O que continua de fora

- **teto de custo em dólar.** Os tokens são registrados; converter para dinheiro
  exige tabela de preço por modelo, que envelhece.
- **limite global de tarefas simultâneas.** São nove telas, e cada uma tem sua
  trava; não há teto agregado.
- **validação de argumento contra o `Schema`** anunciado ao modelo. Campo
  desconhecido é ignorado em silêncio pelas ferramentas — o oposto do que a API
  HTTP faz com `DisallowUnknownFields`.
