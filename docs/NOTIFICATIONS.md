# Notificações: fazer o agente te chamar

O agente para diante de senha, 2FA e CAPTCHA — e essa parada só tem valor se
alguém ficar sabendo. Sem destino configurado, o pedido de take-over entra numa
fila no disco e **fica lá**: a tarefa espera, a tela fica ocupada, e nada
acontece até você olhar por conta própria.

Este documento é o passo a passo para ligar isso com [ntfy](https://ntfy.sh),
do zero, e para verificar que funcionou.

## O que é o ntfy, em três linhas

Um serviço de notificação por HTTP. Você escolhe um nome de **tópico**, e
qualquer `POST` para `https://ntfy.sh/<tópico>` vira notificação em quem estiver
assinando aquele tópico — no celular, no navegador ou no terminal.

Não tem cadastro, não tem chave de API, não tem SDK. O "Hello world" inteiro é:

```bash
curl -d "Olá!" ntfy.sh/agent-computer
```

É por isso que ele foi escolhido aqui: o agente já sabia falar HTTP.

---

## Passo 1 — escolher o tópico

O tópico é só um nome. **Quem sabe o nome lê tudo o que passa por ele e publica
nele** — não existe senha separada.

| Escolha | Quando |
|---|---|
| nome curto (`agent-computer`) | você quer poder mandar um `curl` de qualquer lugar sem consultar nada. É o que está em uso |
| nome longo e aleatório (`agentc-dc6a32…`) | os avisos citam coisa que não deve ser lida por terceiros |
| ntfy com token | você quer fechar de verdade — exige conta no ntfy.sh ou servidor próprio |

O tópico atual é **`agent-computer`**, e a escolha é deliberada: os avisos
citam a tela, o motivo do bloqueio e um trecho da tarefa, e isso foi aceito em
troca de um canal utilizável com um `curl` de uma linha.

Os três casos usam exatamente a mesma configuração — trocar depois é editar uma
linha, sem tocar em código.

## Passo 2 — assinar, para receber

Escolha uma (ou as três; o mesmo tópico serve a todas):

**Celular** — instale o app *ntfy* ([Android](https://play.google.com/store/apps/details?id=io.heckel.ntfy),
[iOS](https://apps.apple.com/us/app/ntfy/id1625396347)), toque em **+**, digite
`agent-computer`, pronto. É o que faz o pedido de take-over chegar em você longe
do computador — que é o ponto do projeto inteiro.

**Navegador** — abra `https://ntfy.sh/agent-computer` e deixe a aba aberta.

**Terminal** — para acompanhar enquanto trabalha:

```bash
curl -s https://ntfy.sh/agent-computer/json | jq -r '.message // empty'
```

## Passo 3 — guardar o destino no cofre

A URL vive no `pass`, e não solta num script, porque é ela que a máquina lê:

```bash
printf '%s\n' "https://ntfy.sh/agent-computer" \
  | pass insert -m -f bassi/agent-computer/ntfy-url
```

## Passo 4 — ligar na máquina

Um comando, que também prova que ficou de pé:

```bash
task notify-setup
```

Ele faz seis coisas, nesta ordem:

| Passo | O quê |
|---|---|
| 1 | grava `/etc/agentd/notify.env` como `root:root 0600` |
| 2 | prova que **o usuário do modelo não lê** o arquivo |
| 3 | entrega um aviso de teste e confere que o ntfy devolveu um id |
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
  ✅ o ntfy aceitou e devolveu um id de mensagem
```

O arquivo que ele grava tem duas linhas:

```ini
AGENT_WEBHOOK=https://ntfy.sh/agent-computer
AGENT_WEBHOOK_FORMAT=ntfy
```

⚠️ **`/etc/agentd/`, e não `/workspace/`.** O segundo é alcançável pelo usuário
do modelo — o destino dos avisos ali seria o modelo escolhendo para onde vão os
próprios pedidos de socorro, e apagar a linha bastaria para ele trabalhar sem
ninguém olhando.

⚠️ **O `FORMAT` não é decorativo.** Sem ele o aviso vai como JSON e chega ao
celular assim:

```
{"task_id":"task-178…","screen":1,"kind":"blocked","reason":"password",…}
```

Com `ntfy`, chega assim:

```
tela 1 precisa de você
tela 1 PRECISA DE VOCÊ: precisa de senha ou passkey — a página de login pede
usuário e senha. Não envie a senha no chat.
```

## Passo 5 — provar que um aviso REAL chega

O passo 4 provou o canal. Isto prova a coisa inteira — uma tarefa que bloqueia
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

---

## Como o aviso chega

| Campo | Vem de | Exemplo |
|---|---|---|
| título | tipo do evento + tela | `tela 1 precisa de você` |
| corpo | **o mesmo texto que aparece na tela do agente** | `tela 1 PRECISA DE VOCÊ: precisa de senha ou passkey — …` |
| prioridade | `high` só para take-over | fura o modo silencioso do celular |
| etiqueta | `raised_hand` · `x` · `white_check_mark` | vira o emoji da notificação |

Só `blocked` e `failed` viram aviso. Avisar de tudo ensina quem recebe a
ignorar, e aí o pedido de take-over — o único que fica parado esperando gente —
se perde no meio.

**Prioridade alta só no que trava a tela** é a mesma ideia, um nível abaixo.

## Quando o aviso é entregue

Um timer do systemd (`agentd-notify.timer`) roda **a cada 5 minutos**, dois
minutos após o boot. Não é entrega instantânea, e é de propósito: a entrega
acontece fora do caminho da tarefa, para um destino lento nunca segurar a trava
da tela enquanto responde.

Para forçar agora:

```bash
ssh root@<maquina> 'systemctl start agentd-notify.service'
```

⚠️ **Não adianta rodar `agentd -notify-drain` na mão** para testar a entrega: o
binário chamado por você não lê `/etc/agentd/notify.env` (`root:root 0600`), e o
comando apenas lista a fila sem consumi-la. Quem lê o arquivo é o systemd, ao
subir a unidade.

## Se algo não chega

| Sintoma | Causa provável | Como confirmar |
|---|---|---|
| nada chega, e a fila cresce | destino não configurado | `agentd -notify-drain` diz "sem destino configurado" |
| a unidade falha | URL errada, ou o ntfy recusou | `journalctl -u agentd-notify -n 20` |
| chega JSON cru | falta `AGENT_WEBHOOK_FORMAT=ntfy` | `ssh root@<maquina> 'cat /etc/agentd/notify.env'` |
| chega no navegador, não no celular | app sem permissão de notificação, ou economia de bateria | teste com `curl -d teste ntfy.sh/agent-computer` |
| a fila **não** esvazia | o destino recusou (fora de 2xx) | é de propósito: o aviso fica para a próxima passada |

A fila só é consumida quando a entrega **se confirma**. Aceitar um 4xx como
sucesso perderia o aviso em silêncio, que é exatamente o que este mecanismo
existe para impedir.

## Trocar de destino depois

Nada aqui é específico do ntfy — `AGENT_WEBHOOK` é uma URL qualquer.

**Outro tópico ntfy** (por exemplo, um nome longo e aleatório):

```bash
printf '%s\n' "https://ntfy.sh/agentc-$(openssl rand -hex 12)" \
  | pass insert -m -f bassi/agent-computer/ntfy-url
task notify-setup && task notify-test
```

**Endpoint próprio**, que prefira os campos crus:

```ini
AGENT_WEBHOOK=https://seu-servico/hook
AGENT_WEBHOOK_FORMAT=raw
```

`raw` envia `{"task_id","screen","kind","reason","detail","summary","message","at"}`.

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
| destino e formato | `/etc/agentd/notify.env` | `root:root 0600` |
| fila de avisos | `/workspace/agent/events/events.jsonl` | `agentd:agent 0600` |
| filas arquivadas | `/workspace/agent/events/events-<data>.jsonl` | idem |
| unidade de entrega | `agentd-notify.service` | roda como `agentd` |
| timer | `agentd-notify.timer` | a cada 5 min |
| código do formato | `internal/adapters/driven/events/webhook_ntfy.go` | — |
| configuração | `scripts/41-setup-notify.sh` (`task notify-setup`) | — |
| prova ponta a ponta | `scripts/42-notify-endtoend.sh` (`task notify-test`) | — |
