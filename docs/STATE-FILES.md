# Os arquivos que o agente usa

Tudo o que o `agentd` lê ou escreve em `/workspace/agent/`, um a um: **para que
serve, quando mexer, como mexer.**

Para **criar** um conector, habilidade ou runner, veja
[`EXTENDING.md`](EXTENDING.md) — aqui está o que cada arquivo é, lá está como
escrever um.

O diretório inteiro vive no **volume durável**, não no disco do droplet — é o que
faz o estado sobreviver a `task destroy`, à troca de sistema operacional e à
reconstrução da máquina. A exceção deliberada é a identidade do cofre, que fica
em `/etc/agentd/` no disco do sistema, e é o que impede uma foto do volume de ser
suficiente para abrir os segredos.

## A matriz

| Arquivo | Papel | Escreve | Lê | **Entra no prompt?** | Dono e modo |
|---|---|---|---|---|---|
| `guardrails.md` | lições de limites já atingidos | `agentd` | `agentd` | **SIM**, no de sistema | `agentd:agent 0640` |
| `skills/<nome>.md` | procedimento salvo, chamado por `/nome` | operador | `agentd` | **SIM**, após o pedido | `agentd:agentd 0644` |
| `connectors/installed/*.yaml` | manifesto de API que vira ferramenta | operador | `agentd` | só o **nome** da ferramenta | `agentd:agentd` |
| `connectors/secrets/*` | credencial de conector | operador | `agentd` | **nunca** | `agentd:agentd 0600` |
| `runners.json` | catálogo de agentes de código | operador | `agentd` | não | `agentd:agent 0640` |
| `pricing.json` | preço por modelo, para o teto de custo | operador | `agentd` | não | `agentd:agent 0640` |
| `progress.md` | desfecho de cada tarefa | `agentd` | pessoas | não | `agentd:agent 0640` |
| `activity.log` | uma linha por iteração | `agentd` | pessoas | não | `agentd:agent 0640` |
| `errors.log` | falha de ferramenta, com repetição | `agentd` | pessoas | não | `agentd:agent 0640` |
| `tasks/<id>.json` | estado de uma tarefa | `agentd` | `agentd` | não | `agentd:agent 0750` |
| `conversations/<id>.json` | histórico completo | `agentd` | `agentd` | **é** o prompt | `agentd:agent 2750` |
| `events/events.jsonl` | fila de avisos | `agentd` | drenador | não | `agentd:agent 0600` |
| `locks/screen-N.lock` | trava de uma tarefa por tela | `agentd`, CLI | ambos | não | `agentd:agent 0660` |
| `status/screen-N.status` | linha desenhada na tela | `agentd` | overlay | não | `agentd:agent 2750` |
| `screens/<N>` | marcador de tela a subir no boot | `screen-add` | `agent-screens` | não | `agentd:agent 2770` |
| `screenshots/` | capturas do navegador | `agentd` | pessoas | não | `agentd:agent 2750` |
| `vault/` | cofre gopass cifrado | `agentd` | `agentd` | **nunca** | `agentd:agentd 0700` |
| `api-token` | token da porta HTTP, cópia do cliente | operador | CLI | não | `agent:agent 0600` |
| `anthropic.env` | credencial do agente de código | operador | `agentd` | **nunca** | `agentd:agent 0600` |

**Só DOIS entram no prompt do modelo**: `guardrails.md` e as habilidades. Os dois
são, na prática, instrução executável — e é por isso que os dois vivem em
diretório do `agentd`, com o usuário do modelo sem escrita. Quem controla o
próprio prompt não está contido.

---

## Os que entram no prompt

### `guardrails.md` — as lições

**Para que serve.** Guardar o que já deu errado nesta máquina, de forma que a
próxima tarefa saiba antes de tentar. É a memória entre tarefas, que não existia.

**Quando é escrito.** Sozinho, quando um detector determinístico dispara — nunca
pelo modelo. Hoje só o detector de ferramenta em laço produz lição; os de teto
(turnos, custo, tempo) bloqueiam sem ensinar nada, porque "você gastou demais"
não é uma lição reaproveitável.

**Como se usa.** Não se usa: o serviço lê e concatena ao prompt de sistema de
toda tarefa nova. Para ver o que o agente está carregando:

```bash
ssh root@<maquina> cat /workspace/agent/guardrails.md
```

**Quando mexer à mão.** Para *remover* uma lição que envelheceu — um site que
mudou, um erro que foi corrigido. Como root, porque nem o `agentd` deve editar o
que ele mesmo escreveu por regra:

```bash
ssh root@<maquina> "vi /workspace/agent/guardrails.md"
```

⚠️ Tem teto de 4 KB. Ao estourar, a lição mais antiga sai. Lição repetida não
duplica — se o mesmo detector disparar de novo, a entrada é atualizada, não
somada.

### `skills/<nome>.md` — os procedimentos

**Para que serve.** Guardar um procedimento que se repete, para não reescrevê-lo
em toda tarefa. O agente o puxa quando o pedido cita `/nome`.

**Quando usar.** Quando você se pegar explicando a mesma coisa duas vezes. O
exemplo instalado, `web-search`, ensina a ordem de fontes para buscar na web — e
existe porque o IP de datacenter é bloqueado pelos buscadores, então a ordem
importa.

**Como instalar.** Pelo catálogo, que valida nome e tamanho:

```bash
agentd -catalog skill-save web-search < examples/skills/web-search.md
agentd -catalog list                     # confere
```

**Como usar numa tarefa.** Cite com barra; o marcador some do texto e o conteúdo
entra depois do pedido:

```bash
agentd -prompt "/web-search qual a cotação do dólar agora?"
```

⚠️ O marcador é removido do prompt, mas um **caminho de arquivo não vira
habilidade**: `/workspace/projects` continua sendo um caminho. A distinção está
em `domain/connector.go`, e existe porque a primeira versão anexava uma
habilidade chamada "workspace" e comia o caminho.

⚠️ Habilidade inexistente é **silenciosa pela porta HTTP** (o aviso só sai no
CLI). Se o comportamento não mudou como esperado, confira o nome com
`-catalog list`.

---

## Os que configuram o comportamento

### `runners.json` — quais agentes de código existem

**Para que serve.** Dizer quais agentes o `delegate_to_code` pode invocar, e com
qual linha de comando exata.

**Quando mexer.** Ao instalar um agente novo na máquina, ou ao mudar as flags de
um existente.

**Como é.** Comando como **vetor**, nunca string — vai direto para
`exec.Command`, sem shell no meio:

```json
{"codex": {"cmd": ["codex","exec","--yolo","-"], "stdin": true,
           "description": "OpenAI Codex"}}
```

`{prompt}` é trocado pelo caminho de um arquivo temporário com a tarefa.
`"stdin": true` manda o texto pela entrada padrão, para os CLIs que a esperam.

**Como usar.** O modelo escolhe pelo nome; sem `runner`, é o Claude Code:

```
delegate_to_code {"task": "...", "runner": "codex"}
```

⚠️ O catálogo **recusa** qualquer comando cujo binário seja interpretador (`sh`,
`bash`, `env`, `xargs`). Com shell no meio voltam `;` e `$(...)`, e com eles
`sudo` — que desfaz o rebaixamento do modelo.

⚠️ Runner cadastrado sem o binário instalado falha **nomeando o binário que
falta**. É deliberado: a mensagem vira a documentação de instalação.

### `pricing.json` — quanto custa cada modelo

**Para que serve.** Converter tokens em dólares, para o teto de custo existir.

**Quando mexer.** Quando o fornecedor mudar o preço, ou ao usar um modelo novo.
**Preço envelhece** — é por isso que a tabela mora aqui e não no binário.

**Como é.** Duas faixas, porque a xAI dobra o preço acima de 200 mil tokens de
prompt, e a origem é obrigatória:

```json
{"grok-4.6": {
  "small_prompt": {"input_per_1m": 2.00, "cached_per_1m": 0.50, "output_per_1m": 6.00},
  "large_prompt": {"input_per_1m": 4.00, "cached_per_1m": 1.00, "output_per_1m": 12.00},
  "source": "docs.x.ai/docs/models, consultado em 2026-08-31"}}
```

⚠️ **Modelo ausente da tabela não bloqueia** — ausência é "não sei", não "de
graça". Os tokens continuam somados, então dá para descobrir depois quanto ele
custou; mas o teto em dólar não vale para ele.

⚠️ Campo com nome errado é **recusado**, e não ignorado: um `input_per_1k` viraria
preço zero, e preço zero desliga o teto em silêncio.

### `connectors/installed/*.yaml` — APIs que viram ferramenta

**Para que serve.** Transformar uma API HTTP em ferramenta que o modelo chama,
com a credencial ficando **fora** do alcance dele: o `agentd` monta a requisição.

**Como instalar.**

```bash
agentd -catalog install examples/connectors/digitalocean.yaml
agentd -catalog secret digitalocean-token     # pede pelo stdin, sem eco
```

**Como usar.** Anexe com `@`; só o conector citado vira ferramenta:

```bash
agentd -prompt "@digitalocean liste meus droplets"
```

⚠️ Anexar é obrigatório de propósito. O catálogo inteiro custaria token a cada
iteração e daria alcance a serviços que a tarefa não pediu.

⚠️ O `base_url` é validado **na leitura e na conexão** — nem IP interno nem nome
que resolva para ele. Ver [`SECURITY.md`](SECURITY.md).

---

## Os que registram o que aconteceu

Nenhum dos três é lido pelo código. Existem para **pessoas** entenderem o que a
máquina fez, e é onde se olha primeiro quando algo saiu estranho.

### `progress.md` — o que cada tarefa deu

Uma linha por tarefa encerrada, com o desfecho:

```
[2026-08-31T00:31:08Z] tarefa=task-178... tela=4 estado=blocked turnos=1
  motivo=guardrail detalhe=a ferramenta shell falhou 1 vezes seguidas...
```

**Quando olhar.** Para saber o que a máquina andou fazendo sem abrir tarefa por
tarefa. É o resumo executivo.

### `activity.log` — o detalhe de cada iteração

```
[2026-08-31T00:41:51Z] tarefa=task-178... tela=3 iteracao=1 turnos=1
  duracao=1.968s tokens=2505/3 cache=512 custo=US$0.0043 parada=stop
  ferramentas=nenhuma
```

**Quando olhar.** Para entender **por que uma tarefa demorou ou custou** —
quantos turnos, quais ferramentas, quanto de cache aproveitou. É a única
observabilidade do laço.

```bash
ssh root@<maquina> "tail -20 /workspace/agent/activity.log"
```

### `errors.log` — as falhas, com contagem

```
[...] ferramenta=shell repeticao=2 saída (com erro: exit status 1): cat: ...
[...] guardrail=ferramenta-em-laco tarefa=... a ferramenta shell falhou 3 vezes
```

**Quando olhar.** Quando uma tarefa parou e você quer a causa em uma linha, sem
abrir a conversa inteira. O campo `repeticao=` mostra o laço se formando.

---

## Os que são estado interno

Não se editam à mão. Estão aqui para quem for diagnosticar.

| Arquivo | O que é | Cuidado |
|---|---|---|
| `tasks/<id>.json` | estado da tarefa, com `TurnsUsed` e `CostUSD` acumulados | editar à mão desalinha o que a reconciliação vê |
| `conversations/<id>.json` | o histórico inteiro, que **é** o prompt | não expira; se o modelo leu um segredo, está aqui (decisão aceita, ver `SECURITY.md`) |
| `locks/screen-N.lock` | a trava; o conteúdo é diagnóstico, quem manda é o `flock` | apagar com tarefa viva permite duas na mesma tela |
| `events/events.jsonl` | fila de avisos, drenada pelo timer | `agentd -notify-drain` lista sem consumir |
| `screens/<N>` | marcador de tela a subir no boot | criado por `screen-add`, removido por `screen-remove` |
| `vault/` | cofre gopass | **inútil sem a identidade** em `/etc/agentd/gopass`, que fica no disco do sistema |

### Por que a tela quebra se `locks/` tiver dono errado

Já aconteceu: depois da separação de usuários, `locks/` ficou com o dono antigo,
e o supervisor lê falha de trava como "tela ocupada". Resultado: **toda** tarefa
respondia 409, e nada indicava permissão. O modo é `0660` porque dois usuários
legítimos tomam a trava — o serviço (`agentd`) e o CLI do operador (`agent`).

---

## Onde cada um nasce

Os arquivos são criados pelo oneshot `agent-state-ownership`
(`nixos/host.nix`), e **não** por `systemd.tmpfiles.rules`.

⚠️ Isto não é escolha de estilo: as regras de tmpfiles sob `/workspace/agent`
são recusadas com `unsafe path transition` — o dono muda de `agent` para
`agentd` no meio do caminho — e **descartadas em silêncio**, sem falhar a
unidade. Diretório novo aqui tem de nascer no oneshot.

## Como conferir tudo de uma vez

```bash
task guardrails-test   # dono, modo, e que o modelo não escreve nos de memória
task validate          # units, telas, e o estado montado do volume
```

A seção 1 do `guardrails-test` afirma dono e modo de cada arquivo de memória, e
a seção 2 prova que o usuário do modelo **não** consegue escrever neles. É a
verificação que transforma esta tabela em fato.
