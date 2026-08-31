# O que se enxerga desta máquina, e por onde

Este documento responde uma pergunta: **quando algo dá errado numa tarefa, onde
se olha?**

Os outros documentos de `docs/` explicam cada peça. Este liga as três camadas de
observação e diz o que cada uma responde — e, mais importante, o que ela **não**
responde.

---

## O ponto de partida

Até 31/08/2026 este repositório não tinha observabilidade nenhuma. Não é
avaliação: o `README.md` classificava o item como **⚠️ fraca** na tabela de
diagnóstico do harness, e `STATE-FILES.md` dizia, textualmente, que um arquivo
de texto plano *"é a única observabilidade do laço"*.

A varredura confirmou: zero OpenTelemetry, zero métricas, zero profiling, nenhum
identificador de correlação, e `service.Agent` — o laço principal — sem sequer um
logger.

O custo aparecia nos números da própria máquina. Em **4 h 13 min** de uso:

| | |
|---|---|
| iterações | 326 |
| falhas de ferramenta | **170** |
| tarefas encerradas | 123 → **91 `done`, 31 `blocked`, 1 `failed`** |

Uma falha a cada duas iterações, um quarto das tarefas bloqueando, e **nenhuma
forma de saber por quê** sem abrir a conversa de cada uma à mão, por SSH.

---

## As três camadas

```
  ┌─ Mac ────────────────────────────────────────────────────┐
  │  nix run .#observability   (task obs:up)                 │
  │                                                          │
  │  Grafana ── VictoriaTraces (traces, API do Jaeger)       │
  │          ├─ VictoriaLogs   (logs e eventos de kernel)    │
  │          └─ VictoriaMetrics                              │
  └──────────────▲───────────────────────────────────────────┘
                 │ PUSH — OTLP e JSON Lines, por túnel ou malha
                 │ (o ingress da máquina segue sendo só a 22)
  ┌──────────────┴─ droplet ─────────────────────────────────┐
  │                                                          │
  │  1. agentd   OTel, convenção gen_ai      o que o agente FEZ
  │  2. agent-probe   eBPF no kernel      o que o modelo EXECUTOU
  │  3. agent-probe   PSI e /proc          por que a máquina DEGRADOU
  └──────────────────────────────────────────────────────────┘
```

### Por que três, e não uma

| Camada | Responde | É cega a |
|---|---|---|
| **agentd** (OTel) | a INTENÇÃO: esta ferramenta, neste turno, custou tanto | o que o sistema de fato fez |
| **agent-probe** (eBPF) | o FATO: este binário rodou, com este pai, neste cgroup | por que ele foi chamado |
| **agent-probe** (PSI) | QUE a máquina degradou, e QUANTO | QUEM causou |

Só as três juntas respondem *"o modelo tentou X, o sistema fez Y, e a máquina
sentiu Z"*.

E há uma assimetria que importa: **só a camada 2 vale como prova.** As outras
duas rodam em espaço de usuário, dentro do alcance do que elas observam. O
modelo de ameaça declarado em [`SECURITY.md`](SECURITY.md) é o próprio modelo, e
instrumentação que o observado pode desligar não é registro — é cortesia.

---

## Camada 1 — o que o agente fez

### O que se vê

Uma tarefa vira uma cascata:

```
agentd.task            3.32s
├─ chat grok-4.6       1.39s
├─ execute_tool shell  29.15ms
└─ chat grok-4.6        1.9s
```

Medido numa tarefa real: dos 3,32 s, **3,29 s foram o modelo e 0,029 s o shell**.
Antes disso, "a tarefa demorou" não se decompunha — o repositório media duração
em exatamente **dois lugares**, e nenhuma ferramenta era cronometrada.

### Onde ela é montada

| Trecho | Abre em |
|---|---|
| `agentd.task` | `service/agent.go` `Run` e `Resume` |
| `chat <modelo>` | `service/agent.go` `complete` |
| `execute_tool <nome>` | `service/toolrun.go` `executeTool` |

Eventos pendurados no trecho da tarefa: `agentd.turn` (turnos e custo
acumulados, turno a turno — a curva, não só o total), `agentd.guardrail.hit`,
`agentd.takeover.requested`.

Atributos pela **convenção GenAI do OpenTelemetry** onde ela existe
(`gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`,
`gen_ai.response.finish_reasons`), e prefixo `agentd.` onde não existe — CDP,
take-over, guardrail. Quem já tem painel construído sobre a convenção enxerga
este agente sem configurar nada.

### O porto, e por que ele é local

`service.Tracer` é definido dentro de `service/telemetry.go`, e **não** em
`ports/`. É o mesmo critério que `GuardrailJournal` e `CostEstimator` já
seguem: `ports/` descreve o que o agente EXIGE do mundo para funcionar, e o
agente funciona inteiro sem telemetria.

O resultado é que `service` e `domain` continuam sem **um único import de
terceiro**. O SDK do OpenTelemetry vive só em
`adapters/driven/telemetry/` e em `cmd/agentd/`.

### 🛑 O que NUNCA vai para um trecho

Prompt, resposta do modelo, comando de shell, URL, conteúdo de página,
argumentos de ferramenta, `GuardrailHit.Detail` bruto.

Nada disso, **redigido ou não**.

O motivo é uma distinção que é fácil perder: a conversa fica no volume, com
permissão de arquivo, e `SECURITY.md` aceita isso explicitamente. **Telemetria
sai da máquina.** São coisas diferentes.

Isto não é conservadorismo local — é o desenho da própria convenção GenAI, onde
`gen_ai.input.messages` é *opt-in explícito*. Aqui simplesmente nunca se liga.

No lugar dos argumentos vai **`agentd.tool.args_hash`**, reaproveitando a chave
que o detector de laço já usa. Dá para ver "é a mesma chamada que falhou três
vezes" sem que o comando escrito pelo modelo saia da máquina.

---

## Camada 2 — o que o modelo executou

Documentada em detalhe em [`KERNEL-VISIBILITY.md`](KERNEL-VISIBILITY.md).

Em uma linha: um programa eBPF atachado a `sched_process_exec` registra **todo
`execve` da máquina**, com o caminho completo do binário, o usuário, o PID e o
cgroup — no kernel, fora do alcance do usuário `agent`.

---

## Camada 3 — por que a máquina degradou

`/proc/pressure` (PSI) e `/proc/meminfo`, amostrados a cada 30 s pelo mesmo
binário do coletor.

**Isto não é eBPF, e dizer isso é parte do desenho.** O kernel já calcula a
pressão; uma probe que a recalculasse a partir de eventos seria mais cara e
menos precisa. Vender eBPF como substituto de PSI seria a mesma inflação que
este repositório já recusou uma vez, ao medir −82% de memória no KasmVNC e
decidir não trocar porque nada estava limitando.

Uma amostra real desta máquina:

```
cpu=0.19 mem=0.00 io=0.00 mem_usado=26%
mem_disponivel=2979240 kB  swap_livre=4103672 kB
```

⚠️ A fração de memória vem de **`MemAvailable`**, nunca de `MemFree`. Numa
máquina saudável o `MemFree` é sempre baixo, porque o kernel usa a RAM ociosa
como cache — um painel construído sobre ele alarmaria todo dia.

E a pressão é lida da linha **`some`**, não da `full`: `some` conta quando
ALGUMA tarefa esperou, `full` quando TODAS esperaram. A segunda quase nunca
dispara numa máquina com trabalho variado, e alerta que quase nunca dispara é
alerta que ninguém confere.

---

## O elo entre as camadas

| De | Para | Como |
|---|---|---|
| `activity.log` | a cascata no Grafana | `trace_id=` e `span_id=` no fim de cada linha |
| trecho da ferramenta | os `execve` que ela disparou | PID e janela de tempo |
| amostra de saúde | o que rodava na hora | mesmo fluxo, campo `kind` |

O carimbo no `activity.log` vai no **fim** da linha, e isso é deliberado: o
formato já é `chave=valor` separado por espaço, então dois campos a mais não
quebram quem faz `tail`; e o começo da linha é onde o olho procura a tarefa e a
tela.

Sem trace configurado, o carimbo **não aparece** e a linha sai exatamente como
saía antes — o que mantém o arquivo íntegro na máquina sem telemetria, que é
justamente quando ele é a única coisa que resta.

### Por que o journal não virou JSONL

Foi considerado e recusado. Ele é a verdade que funciona quando o pipeline não é
alcançável — e o nó desta máquina esteve **offline na malha por um dia** durante
o próprio trabalho de implementação, o que torna a hipótese concreta. Texto
plano sem dependência de parser é exatamente a garantia que se quer no caso em
que tudo mais falhou.

---

## Como subir

```bash
task obs:up          # no Mac: Grafana + VictoriaTraces + VictoriaLogs + VictoriaMetrics
task obs:status      # pergunta a cada porta se ela responde
task obs:open        # abre o Grafana

task probe:deploy    # compila o objeto BPF e instala o coletor na máquina
task probe:test      # prova que ele vê o execve, com prova de falha
task probe:run       # roda em primeiro plano, imprimindo cada execve
```

### O transporte

A máquina **empurra**; nada nela escuta. É o que preserva o invariante que
`08-validate.sh` já testa: toda porta em `127.0.0.1`, e só a 22 no firewall. Um
coletor com endpoint de scrape quebraria as duas coisas.

Dois caminhos, e o segundo é o de contingência:

```bash
# pela malha, quando o Tailscale da máquina está autenticado
tailscale up   # na máquina; o login é interativo por decisão do repositório

# por túnel reverso, que é o que funciona hoje
ssh -N -f -R 4317:127.0.0.1:4317 -R 9428:127.0.0.1:9428 root@<ip>
```

⚠️ Expor as portas do backend no IP público **não** é alternativa: elas não têm
autenticação, por serem de laboratório em loopback.

### Ligando o agente

```bash
# na unidade systemd, via host.nix
AGENTD_OTLP_ENDPOINT=127.0.0.1:4317

# o destino dos eventos de kernel, num arquivo que o modelo não alcança
/etc/agent-probe/sink.url
```

---

## Métricas — a terceira forma de olhar

Trace responde "o que aconteceu nesta tarefa". Métrica responde "como as tarefas
vêm se comportando". Quem tenta responder a segunda somando traces descobre
tarde que o backend não foi feito para isso.

| Instrumento | Rótulos | Responde |
|---|---|---|
| `agentd.model.tokens` | `model`, `token.type` | quanto de cada tipo — e o cache custa 4× menos |
| `agentd.model.cost.usd` | `model` | o gasto do mês, que antes só existia dentro de cada tarefa |
| `agentd.turn.duration` | `model`, `stop_reason` | distribuição do tempo do modelo |
| `agentd.tool.duration` | `tool.name`, `failed` | **o instrumento que não existia** |
| `agentd.guardrail.hits` | `kind` | qual detector vem disparando |
| `agentd.task.outcomes` | `task.state` | 31 de 123 `blocked`, agora como série |
| `agentd.tasks.running` | — | quantas rodam **agora** |

### 🛑 A regra de cardinalidade

Nenhum rótulo pode ter valor ilimitado. `task.ID` é `task-<UnixNano>`, único por
execução: como rótulo criaria uma série **por tarefa**, e séries não são
apagadas — o custo seria permanente e cresceria para sempre.

Ele vai no trecho, onde é barato. Há um teste dedicado a isso
(`TestNoMetricLabelCarriesTaskID`), que varre todos os rótulos de todas as
medidas de uma tarefa real.

### Dois endpoints, e o porquê

```
AGENTD_OTLP_ENDPOINT=127.0.0.1:4317          # traces  -> VictoriaTraces (gRPC)
AGENTD_OTLP_METRICS_ENDPOINT=127.0.0.1:8428  # métricas -> VictoriaMetrics (HTTP)
```

Apontar os dois para o mesmo lugar produz um erro que parece de rede e não é:

```
rpc error: code = Unimplemented
desc = gRPC method not found: .../MetricsService/Export
```

O VictoriaTraces implementa só o serviço de traces.

### ⚠️ O nome da métrica no VictoriaMetrics preserva os pontos

Medido em 31/08/2026, e custa um painel vazio para descobrir: o VictoriaMetrics
**não** converte ponto em `_` nem acrescenta `_total`, como a convenção
Prometheus faria. Uma consulta escrita naquela convenção devolve vazio **sem
erro** — o painel parece sem dado, em vez de com consulta errada.

```promql
agentd_model_tokens_total          # vazio, sem erro
{__name__="agentd.model.tokens"}   # certo
```

O mesmo vale para os rótulos: `sum by ("agentd.token.type") (...)`, com aspas.

---

## O painel

`observability/dashboard-agent-computer.json`, provisionado em arquivo junto com
as fontes de dados. Painel montado na tela vive no SQLite do Grafana, que
ninguém versiona: some no primeiro `rm -rf data/`, e alguém o refaz de memória
com uma consulta ligeiramente diferente.

As três camadas numa tela só. Dois detalhes que custaram para acertar:

- **`collapsed: false` e `panels: []` explícitos** nas linhas. Sem eles o Grafana
  trata a linha como colapsada e os painéis somem — sem erro, e sem nada que
  indique a causa.
- **`increase` sobre `$__range`, não `rate` de 5 min** no histograma. Com `rate`
  o painel fica vazio numa máquina de uso esporádico: são necessários dois
  pontos na janela, e o exportador envia a cada 30 s — então ele só mostraria
  algo enquanto há trabalho acontecendo, que é justamente quando ninguém está
  olhando.

## O que ainda falta

| Item | Por quê |
|---|---|
| Trecho por iteração do laço | exigiria reescrever o corpo do `for` — são oito pontos de saída, e um `defer` não fecha no lugar certo. O evento `agentd.turn` entrega a curva sem a cirurgia |
| Envio dos logs do `agentd` ao VictoriaLogs | o formato JSON já está pronto; falta o transporte do journald |
| Filtrar a própria conexão do coletor | ele registra o próprio envio (`agent-probe -> 127.0.0.1:9428`). É ruído previsível, e filtrá-lo tem o risco de filtrar demais |
| Probes de `fork`, sinal e OOM | dariam a linhagem de processo e a causa de morte; o desenho está em `KERNEL-VISIBILITY.md` |
