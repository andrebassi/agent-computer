# Notificações: fazer o agente te chamar

O agente para diante de senha, 2FA e CAPTCHA — e essa parada só tem valor se
alguém ficar sabendo. Sem destino configurado, o pedido de take-over entra numa
fila no disco e **fica lá**: a tarefa espera, a tela fica ocupada, e nada
acontece até você olhar por conta própria.

Este documento é o passo a passo para ligar isso, do zero, com os **dois
destinos em uso** — e para verificar que funcionou.

## Os dois destinos, e por que são dois

| Destino | Formato | Serve para | Vida útil |
|---|---|---|---|
| [**ntfy.sh**](https://ntfy.sh) | `ntfy` (texto) | **agir** — a notificação chega ao seu celular | permanente |
| [**WebhookInbox**](https://webhookinbox.com) | `raw` (JSON) | **depurar** — guarda corpo e cabeçalhos da requisição | **expira em 1 h**, ver B.3 |

Eles atendem leitores diferentes. O ntfy entrega a frase que você lê no celular
às três da manhã; o WebhookInbox guarda
`{"task_id":…,"screen":1,"kind":"blocked"}` com os cabeçalhos, que é o que
responde *"o agente está mandando o que eu acho que ele manda?"*. Escolher um
perderia o outro.

O resto do documento é **Parte A** (ntfy), **Parte B** (WebhookInbox),
**Parte C** (ligar os dois na máquina) e **Parte D** (testar e diagnosticar).

---

# Parte A — ntfy, a notificação que chega em você

## A.1 O que é, em três linhas

Um serviço de notificação por HTTP. Você escolhe um nome de **tópico**, e
qualquer `POST` para `https://ntfy.sh/<tópico>` vira notificação em quem estiver
assinando aquele tópico — celular, navegador ou terminal.

Sem cadastro, sem chave de API, sem SDK. O "Hello world" inteiro é:

```bash
curl -d "Olá!" ntfy.sh/agent-computer
```

## A.2 Escolher o tópico

O tópico é só um nome. **Quem sabe o nome lê tudo o que passa por ele e publica
nele** — não existe senha separada.

| Escolha | Quando |
|---|---|
| nome curto (`agent-computer`) | você quer mandar um `curl` de qualquer lugar sem consultar nada. **É o que está em uso** |
| nome longo e aleatório | os avisos citam coisa que não deve ser lida por terceiros |
| ntfy com token | fechar de verdade — exige conta no ntfy.sh ou servidor próprio |

A escolha por `agent-computer` é deliberada e tem preço: os avisos citam a tela,
o motivo do bloqueio e um trecho da tarefa, e quem tentar o nome lê isso — e pode
publicar um aviso falso ali. Trocar depois é editar uma linha, sem tocar em
código.

## A.3 Assinar, para receber

Escolha uma (ou as três; o mesmo tópico serve a todas):

**Celular** — instale o app *ntfy*
([Android](https://play.google.com/store/apps/details?id=io.heckel.ntfy),
[iOS](https://apps.apple.com/us/app/ntfy/id1625396347)), toque em **+**, digite
`agent-computer`. É o que faz o pedido de take-over te alcançar longe do
computador — que é o ponto do projeto inteiro.

**Navegador** — abra `https://ntfy.sh/agent-computer` e deixe a aba aberta.

**Terminal** — acompanhar ao vivo, ou ler o que já passou:

```bash
curl -s https://ntfy.sh/agent-computer/json | jq -r '.message // empty'

curl -s "https://ntfy.sh/agent-computer/json?poll=1&since=1h" \
  | jq -r 'select(.event=="message") | "[p\(.priority // "-")] \(.title // "") — \(.message)"'
```

## A.4 Provar que o tópico responde

```bash
curl -d "teste" ntfy.sh/agent-computer
```

Se não aparecer no celular mas aparecer no navegador, o problema é permissão de
notificação ou economia de bateria no aparelho — não o canal.

---

# Parte B — WebhookInbox, o coletor que mostra o JSON cru

## B.1 O que é

Um coletor descartável: aceita qualquer `POST` e guarda **corpo, cabeçalhos, IP
e horário**, expostos por uma API. Serve para ver o que o agente realmente manda
sem escrever endpoint nenhum.

A inbox em uso é `zOkMqPRA`:

```
postar:  https://api.webhookinbox.com/i/zOkMqPRA/in/
ler:     https://api.webhookinbox.com/i/zOkMqPRA/items/
```

## B.2 Criar a sua

Uma chamada, sem cadastro:

```bash
curl -sS -X POST https://api.webhookinbox.com/create/
```

```json
{"id": "D3OxcLnu", "base_url": "https://api.webhookinbox.com/i/D3OxcLnu/",
 "ttl": 3600, "response_mode": "auto"}
```

O endereço de POST é o `base_url` **mais `in/`**. Guarde o `base_url`: não há
listagem de inboxes, e uma inbox cujo id se perdeu não se recupera.

## B.3 ⚠️ A inbox EXPIRA — `ttl: 3600`

Uma hora. É o campo que a própria API devolve, e é a diferença mais importante
em relação ao ntfy: **um dia depois de configurar, esse destino provavelmente
não existe mais.**

O que isso causa — e por que não é grave aqui:

| | |
|---|---|
| o POST passa a falhar | o ntfy continua entregando |
| a fila **não** trava | o desenho limpa a fila quando **pelo menos um** destino aceita |
| aparece no log | `⚠️ entregue a 1 destino(s); falhou em: …` |

Ou seja: a inbox expirada degrada o **diagnóstico**, não o aviso. Foi escolhido
assim de propósito — ver [C.3](#c3-quando-um-dos-destinos-falha).

Renovar antes de expirar:

```bash
curl -sS -X POST https://api.webhookinbox.com/i/zOkMqPRA/refresh/
```

Para usar por muito tempo, vale um `cron` diário com esse `curl`. A alternativa
honesta é tratá-lo como o que ele é: um destino de depuração temporário, que se
recria com uma chamada.

⚠️ **O serviço devolve `502` de vez em quando.** Medido em 31/08/2026: o mesmo
`refresh` deu 502 numa chamada e 200 nas duas seguintes, e um `GET /items/`
falhou o parse de JSON no mesmo instante. Não é configuração errada — é
instabilidade do serviço público. Repita antes de investigar.

Para o agente, essa instabilidade é inofensiva pelo mesmo motivo da expiração:
o ntfy entrega, a fila é limpa, e a falha vira uma linha no log.

## B.4 Ler o que chegou

```bash
curl -s "https://api.webhookinbox.com/i/zOkMqPRA/items/?order=-created&max=5" | jq .
```

Só o corpo dos avisos, que é o que interessa na maior parte das vezes:

```bash
curl -s "https://api.webhookinbox.com/i/zOkMqPRA/items/?order=-created&max=5" \
  | jq -r '.items[] | "\(.created)  \(.body)"'
```

Exemplo real desta máquina:

```
2026-08-31T03:47:24Z  {"task_id":"task-1788148033427214499","screen":1,
  "kind":"blocked","reason":"guardrail","detail":"a tarefa já custou US$ 0.0016
  em inferência (teto US$ 0.0005, somando as retomadas) e parou…","message":"tela 1
  PRECISA DE VOCÊ: um limite de segurança foi atingido — …","at":"2026-08-31T03:47:24Z"}
```

Os campos do formato `raw`: `task_id`, `screen`, `kind`, `reason`, `detail`,
`summary`, `message`, `at`.

---

# Parte C — ligar os dois na máquina

## C.1 Guardar os destinos no cofre

Os dois numa entrada só, separados por vírgula, cada um com o prefixo do seu
formato:

```bash
printf '%s\n' "ntfy=https://ntfy.sh/agent-computer,raw=https://api.webhookinbox.com/i/zOkMqPRA/in/" \
  | pass insert -m -f bassi/agent-computer/ntfy-url
```

**Regras da lista:**

| Escrita | Vira |
|---|---|
| `ntfy=<url>` | aquele destino em formato ntfy |
| `raw=<url>` | aquele destino em JSON |
| `<url>` sem prefixo | usa o `AGENT_WEBHOOK_FORMAT` |
| espaço em volta, item vazio, vírgula sobrando | ignorados |

⚠️ **URL com `=` na query não é confundida com prefixo.**
`https://x/in/?token=abc` continua inteira: o prefixo só vale quando o que vem
antes do `=` **é** um formato conhecido (`ntfy` ou `raw`). Sem essa checagem, o
destino viraria `abc` — lixo silencioso.

## C.2 Aplicar na máquina

Um comando, que também prova que ficou de pé:

```bash
task notify-setup
```

Ele faz seis coisas, nesta ordem:

| Passo | O quê |
|---|---|
| 1 | grava `/etc/agentd/notify.env` como `root:root 0600` |
| 2 | prova que **o usuário do modelo não lê** o arquivo |
| 3 | entrega um aviso de teste a **cada destino** e confere o código de cada um |
| 4 | **arquiva** a fila acumulada, para o canal não estrear despejando tudo |
| 5 | roda a unidade de drenagem e mostra o resultado |
| 6 | mostra o que sobrou na fila |

Saída esperada, com `erros: 0` no fim:

```
=== 1. gravando o destino na maquina ===
  arquivo: root:root 600
  ✅ so o root le o destino

=== 2. o usuario do modelo NAO le o destino ===
  ✅ recusado por permissao (a mensagem, nao o codigo de saida)

=== 3. entrega de teste, com um aviso de verdade ===
  ✅ aceitou (HTTP 200): https://ntfy.sh/agent-computer…
  ✅ aceitou (HTTP 200): https://api.webhookinbox.com/i/zOkMqPRA/in/…
  destinos: 2 de 2 aceitaram
```

O arquivo que ele grava tem duas linhas:

```ini
AGENT_WEBHOOK=ntfy=https://ntfy.sh/agent-computer,raw=https://api.webhookinbox.com/i/zOkMqPRA/in/
AGENT_WEBHOOK_FORMAT=ntfy
```

⚠️ **`/etc/agentd/`, e não `/workspace/`.** O segundo é alcançável pelo usuário
do modelo — o destino dos avisos ali seria o modelo escolhendo para onde vão os
próprios pedidos de socorro, e apagar a linha bastaria para ele trabalhar sem
ninguém olhando.

⚠️ **O formato não é decorativo.** Sem `ntfy=`, o aviso vai como JSON para o
ntfy e chega ao celular assim:

```
{"task_id":"task-178…","screen":1,"kind":"blocked","reason":"password",…}
```

Com `ntfy`, chega assim:

```
tela 1 precisa de você
tela 1 PRECISA DE VOCÊ: precisa de senha ou passkey — a página de login pede
usuário e senha. Não envie a senha no chat.
```

## C.3 Quando um dos destinos falha

**A fila é limpa se pelo menos um destino aceitar.** Exigir que todos aceitem
parece mais rigoroso e é pior na prática: um destino permanentemente quebrado —
a inbox expirada, por exemplo — seguraria o aviso, e a cada 5 minutos o destino
**bom** receberia a mesma notificação de novo, até quem recebe silenciar o canal.

| Situação | Fila | Unidade |
|---|---|---|
| todos aceitaram | limpa | `success` |
| **parte** aceitou | limpa | `success`, com `⚠️ entregue a N destino(s); falhou em: …` no log |
| ninguém aceitou | **intacta**, tenta na próxima passada | `failed` |

O objetivo é a pessoa ficar sabendo. Se um destino entregou, ela soube.

---

# Parte D — testar e diagnosticar

## D.1 Provar que um aviso REAL chega

A Parte C provou o canal. Isto prova a coisa inteira — uma tarefa que bloqueia
de verdade, o evento entrando na fila, e o drenador entregando:

```bash
task notify-test
```

```
=== 2. uma tarefa que BLOQUEIA de verdade ===
  estado final: tela 1: PRECISA DE VOCÊ — um limite de segurança foi atingido
  ✅ a tarefa task-1788146746377211193 parou no teto, como esperado

=== 3. o evento entrou na fila ===
  pendentes: 1
  ✅ o bloqueio virou aviso enfileirado

=== 4. o drenador entrega e ESVAZIA a fila ===
  ✅ a fila esvaziou: a entrega foi CONFIRMADA pelo destino

erros: 0
```

A distinção entre os dois testes importa: um `curl` que funciona e **um agente
que avisa** são coisas diferentes. Entre eles há a fila, a unidade do systemd, o
usuário do serviço e o formato — e cada um desses já quebrou em silêncio neste
projeto.

## D.2 Conferir os dois destinos, lado a lado

Depois do teste acima, o **mesmo** aviso deve aparecer nos dois, cada um no seu
formato:

```bash
# ntfy: texto, com título e prioridade
curl -s "https://ntfy.sh/agent-computer/json?poll=1&since=10m" \
  | jq -r 'select(.event=="message") | "[p\(.priority // "-")] \(.title // "") — \(.message)"'

# WebhookInbox: JSON cru, com os campos
curl -s "https://api.webhookinbox.com/i/zOkMqPRA/items/?order=-created&max=2" \
  | jq -r '.items[] | "\(.created)  \(.body)"'
```

Foi assim que a configuração atual foi validada:

```
ntfy          [p4] tela 1 precisa de você — tela 1 PRECISA DE VOCÊ: um limite de
              segurança foi atingido — a tarefa já custou US$ 0.001…
webhookinbox  {"task_id":"task-1788148033427214499","screen":1,"kind":"blocked",
              "reason":"guardrail","detail":"a tarefa já custou US$ 0.0016…
```

## D.3 Como o aviso chega

| Campo | Vem de | Exemplo |
|---|---|---|
| título | tipo do evento + tela | `tela 1 precisa de você` |
| corpo | **o mesmo texto que aparece na tela do agente** | `tela 1 PRECISA DE VOCÊ: precisa de senha ou passkey — …` |
| prioridade | `high` só para take-over | fura o modo silencioso do celular |
| etiqueta | `raised_hand` · `x` · `white_check_mark` | vira o emoji da notificação |

Só `blocked` e `failed` viram aviso. Avisar de tudo ensina quem recebe a
ignorar, e aí o pedido de take-over — o único que fica parado esperando gente —
se perde no meio. **Prioridade alta só no que trava a tela** é a mesma ideia, um
nível abaixo.

## D.4 Quando o aviso é entregue

Um timer do systemd (`agentd-notify.timer`) roda **a cada 5 minutos**, dois
minutos após o boot. Não é entrega instantânea, e é de propósito: acontece fora
do caminho da tarefa, para um destino lento nunca segurar a trava da tela
enquanto responde.

Para forçar agora:

```bash
ssh root@<maquina> 'systemctl start agentd-notify.service'
```

⚠️ **Não adianta rodar `agentd -notify-drain` na mão** para testar a entrega: o
binário chamado por você não lê `/etc/agentd/notify.env` (`root:root 0600`), e o
comando apenas lista a fila sem consumi-la. Quem lê o arquivo é o systemd, ao
subir a unidade.

## D.5 Se algo não chega

| Sintoma | Causa provável | Como confirmar |
|---|---|---|
| nada chega, e a fila cresce | destino não configurado | `agentd -notify-drain` diz "sem destino configurado" |
| a unidade falha | URL errada, ou **todos** recusaram | `journalctl -u agentd-notify -n 20` |
| `⚠️ entregue a 1 destino(s)` no log | um destino caiu — em geral a inbox expirada | recrie (B.2) e atualize o cofre (C.1) |
| chega JSON cru no celular | falta o prefixo `ntfy=` naquele destino | `ssh root@<maquina> 'cat /etc/agentd/notify.env'` |
| chega no navegador, não no celular | app sem permissão, ou economia de bateria | `curl -d teste ntfy.sh/agent-computer` |
| a fila **não** esvazia | nenhum destino aceitou | é de propósito: o aviso fica para a próxima passada |
| o teste diz "1 de 1" com 2 configurados | `ssh` dentro de laço engolindo o stdin | já corrigido com `< /dev/null`; se voltar, é isto |
| `502` ou JSON inválido do WebhookInbox | instabilidade do serviço público, não configuração | repita a chamada; medido em 31/08/2026 |

## D.6 Trocar de destino depois

Nada aqui é específico do ntfy nem do WebhookInbox — cada item é uma URL.

**Outro tópico ntfy** (por exemplo, um nome longo e aleatório):

```bash
printf '%s\n' "ntfy=https://ntfy.sh/agentc-$(openssl rand -hex 12)" \
  | pass insert -m -f bassi/agent-computer/ntfy-url
task notify-setup && task notify-test
```

**Endpoint próprio**, que prefira os campos crus:

```ini
AGENT_WEBHOOK=raw=https://seu-servico/hook
```

**Slack, Teams, Telegram** precisam de um formato próprio, que ainda não existe
— cada um espera um corpo diferente (`{"text":…}` no Slack, `{"chat_id","text"}`
no Telegram). Acrescentar um é uma função em
`internal/adapters/driven/events/webhook_ntfy.go` e uma entrada em
`ParseWebhookFormat`; o resto do caminho já está pronto.

⚠️ Formato desconhecido **cai em `raw`** em vez de virar erro: um valor digitado
errado não pode impedir a entrega. Entregar feio é ruim; perder o aviso é pior.

---

## Onde cada peça vive

| Peça | Onde | Dono |
|---|---|---|
| destinos e formato | `/etc/agentd/notify.env` | `root:root 0600` |
| a mesma lista, no cofre | `pass bassi/agent-computer/ntfy-url` | — |
| fila de avisos | `/workspace/agent/events/events.jsonl` | `agentd:agent 0600` |
| filas arquivadas | `/workspace/agent/events/events-<data>.jsonl` | idem |
| unidade de entrega | `agentd-notify.service` | roda como `agentd` |
| timer | `agentd-notify.timer` | a cada 5 min |
| formato ntfy | `internal/adapters/driven/events/webhook_ntfy.go` | — |
| entrega a vários destinos | `internal/adapters/driven/events/webhook_multi.go` | — |
| configuração | `scripts/41-setup-notify.sh` (`task notify-setup`) | — |
| prova ponta a ponta | `scripts/42-notify-endtoend.sh` (`task notify-test`) | — |
