# agent-computer

Desktop virtual persistente no DigitalOcean para agentes autônomos — reprodução
em infraestrutura própria do modelo descrito em
[docs.x.ai/grok-bot/computer-and-apps](https://docs.x.ai/grok-bot/computer-and-apps).

O Grok Bot Computer é serviço hospedado da xAI: a doc não publica API, endpoint
nem pacote, então não há o que instalar. O que existe aqui é a **mesma
arquitetura**, montada do zero e validada contra a doc item por item.

## Estado

**Validado** em 2026-08-30. Todas as suítes fecham com `erros: 0`:

| Suíte | O que prova |
|---|---|
| `task validate` | 10 seções: units, volume, fronteira, portas, firewall, X, Chrome, noVNC, pixel, agente de código |
| `task integration-test` | **12 seções na máquina real, contra o Grok real**: do estado durável ao take-over, com conector e habilidade |
| `scripts/09-persistence-test.sh` | reboot real: serviços sobem sozinhos, sessão do navegador sobrevive |
| `scripts/12-update-test.sh` | rebuild real: `/workspace` sobrevive, `/scratch` e pacote manual somem |
| `agent/scripts/coverage-gate.sh` | 91,4% de cobertura, domínio em 100% |

Lab — serve para testar o conceito, não para produção.

## Índice

| Seção | O que traz |
|---|---|
| [Como usar, com um caso real](#como-usar-com-um-caso-real-do-começo-ao-fim) | **Comece por aqui se quer VER funcionando** |
| [Receituário de exemplos](#receituário-exemplos-que-rodam) | **todo comando que dá para dar**, com o que volta de cada um |
| [O percurso de uma tarefa](docs/TASK-LIFECYCLE.md) | **como as peças se encaixam** — do pedido ao arquivo gravado |
| [Arquitetura ponta a ponta](#arquitetura-ponta-a-ponta) | o modelo explicado, as 12 cláusulas com código e prova |
| [Auditoria de fidelidade](#auditoria-de-fidelidade-à-documentação) | placar do que existe e do que falta |
| [Avaliação do KasmVNC](#avaliação-do-kasmvnc) | medição, e por que não trocar agora |
| [Avaliação do CloakBrowser](#avaliação-do-cloakbrowser) | por que evasão de anti-bot não entra aqui |
| [Loop engineering, porta HTTP e proatividade](#loop-engineering-porta-http-e-proatividade) | **4 defeitos de produção corrigidos**, e o porquê de cada decisão |
| [Guardrails do laço](docs/GUARDRAILS.md) | **os tetos que param o agente**, e por que o ralph não os tem |
| [Estender o agente](docs/EXTENDING.md) | **criar conector, habilidade e runner** — contrato, passo a passo e armadilhas |
| [Os arquivos que o agente usa](docs/STATE-FILES.md) | matriz de todo estado: para que serve, quando e como mexer |
| [`examples/`](examples/README.md) | conectores e habilidades prontos |


### Em uma frase

Você manda uma tarefa, fecha o laptop, e o agente executa no computador em
nuvem — parando e te chamando quando esbarra em senha, 2FA ou CAPTCHA.

```bash
task up                                    # sobe o computador
task open                                  # vê a tela ao vivo
agentd -prompt "@digitalocean audite a conta e escreva o relatório"
task snapshot && task destroy              # guarda e derruba: US$ 26 → US$ 2/mês
```

## Como se verifica que está funcionando

Cinco camadas, e cada uma pega o que a anterior não alcança. O mapa completo de
**funcionalidade × teste** está em [`docs/TEST-MAP.md`](docs/TEST-MAP.md).

```bash
task lint             # 5 gates: variável órfã, API sem token, trava, mensagem inexistente
task nixos:validate   # config NixOS: sintaxe, ASCII, sistema inteiro
task test:cov         # cobertura ≥90% de statements, domínio 100%, com -race
task suites           # 4 suítes de máquina (43 seções)
task functional       # 3 testes que CHAMAM O MODELO de verdade
task hostile          # entrada malformada, degradação, concorrência
task guardrails-test  # detector bloqueia, lição chega ao prompt, modelo não escreve
```

A regra que o mapa aplica: **uma funcionalidade tem cobertura quando existe um
teste que falha se ela for removida.** Contar que algo "existe" não é cobertura —
foi assim que `claude --version` passou por prova de que a delegação funciona,
quando prova apenas que o binário executa.

Cada camada achou o que as outras não achariam:

| Camada | Achou |
|---|---|
| máquina | sudoers descartado inteiro; `locks/` com dono errado pondo toda tela em 409; `agentd-notify` como usuário errado quebrando a proatividade em silêncio |
| funcional | **`panic` por ponteiro nulo** derrubando o binário; trava `0644` que `flock` não abre; três scripts de teste quebrados, um deles **passando** com a verificação vazia |
| hostil | campo desconhecido aceito com 201 (um `"screens"` em vez de `"screen"` ia para a tela errada em silêncio) |
| boot | **`agentd-api` não subia depois do reboot** — perdeu o `wantedBy` na migração; sistema `running`, zero unidades em falha, porta fora do ar |
| a própria infra de teste | **duas suítes concorrentes** contra a mesma máquina — log entrelaçado, `erros: 1` mentiroso numa e `erros: 0` sem valor na outra; fechado por `scripts/suite-lock.sh` |

## Receituário: exemplos que rodam

Tudo abaixo é comando real, copiável. Os que tocam a máquina rodam pelo
`agentd_run` dos scripts (que executa como `agentd`, o usuário que lê o cofre) ou
por `task`.

### Tarefa simples

```bash
# do Mac, pelo script (roda na máquina, como agentd)
source scripts/lib.sh && load_token
agentd_run '-screen 1 -prompt "Qual a cotação do dólar agora?"'

# direto na máquina
agentd -screen 1 -prompt "Liste os arquivos de /workspace/projects"
```

### Com habilidade: `/nome`

```bash
agentd_run '-screen 1 -prompt "/web-search quem joga no Brasileirão hoje?"'
agentd_run '-screen 2 -prompt "/web-diagnosis o site exemplo.com está fora do ar"'
```

O marcador some do texto; o conteúdo da habilidade entra **depois** do pedido.
Habilidade inexistente é silenciosa pela porta HTTP — confira com
`agentd -catalog list`.

### Com conector: `@nome`

```bash
agentd_run '-screen 1 -prompt "@digitalocean liste meus droplets e o custo mensal"'
agentd_run '-screen 1 -prompt "@gitlab abra uma issue no projeto 123 com título Falha no deploy"'

# dois conectores na mesma tarefa
agentd_run '-screen 1 -prompt "@digitalocean @gitlab compare o custo do droplet e abra uma issue com o número"'
```

Só o conector **anexado** vira ferramenta: o catálogo inteiro custaria token a
cada iteração e daria alcance a serviços que a tarefa não pediu.

### Habilidade + conector juntos

```bash
agentd_run '-screen 1 -prompt "@digitalocean /web-search compare o preço do meu droplet com a média de mercado"'
```

### Delegação a um agente de código

```bash
# padrão: Claude Code
agentd_run '-screen 1 -prompt "Use delegate_to_code para criar um script Python que soma dois números, com teste"'

# escolhendo o runner
agentd_run '-screen 3 -prompt "Use delegate_to_code com runner=opencode e a tarefa: crie /workspace/projects/oi.txt com a palavra FUNCIONOU"'
```

Os quatro runners cadastrados, e o que acontece com cada um (medido em
31/08/2026):

| `runner=` | Resultado |
|---|---|
| omitido | Claude Code, como sempre |
| `opencode` | ✅ funciona — grava o arquivo pedido como `agent:agent 644` |
| `codex` | ❌ o CLI quer login de conta ChatGPT; a chave de API não serve |
| `droid`, `kiro` | ❌ não instalados — falha **nomeando o binário que falta** |

```bash
$ agentd_run '-screen 3 -prompt "Use delegate_to_code com runner=opencode e a
              tarefa: crie /workspace/projects/opencode-vivo.txt com FUNCIONOU"' 2>&1
# → runner "opencode" terminou.
$ ssh root@<maquina> 'ls -l /workspace/projects/opencode-vivo.txt; cat "$_"'
-rw-r--r-- 1 agent agent 10 Aug 30 23:12 /workspace/projects/opencode-vivo.txt
FUNCIONOU
```

O dono `agent` na saída é a prova de que a delegação roda **rebaixada** — o
runner herda o usuário do modelo, não o do `agentd`.

Runner fora do catálogo devolve a lista dos que existem, em vez de "não
funcionou":

```
runner "gpt5" não está no catálogo; disponíveis: claude, codex, droid, opencode
```

Contrato completo, credencial por runner e `HOME` próprio em
[`EXTENDING.md`](docs/EXTENDING.md).

### Navegador

O agente pilota o Chrome da própria tela — não há flag, são ferramentas que ele
escolhe usar:

```bash
agentd_run '-screen 1 -prompt "Abra https://news.ycombinator.com, leia os 5 primeiros títulos e resuma"'
agentd_run '-screen 1 -prompt "Abra https://example.com, clique em Learn more e diga onde foi parar"'
agentd_run '-screen 1 -prompt "Tire uma captura da tela atual e descreva o que aparece"'
```

Diante de senha, 2FA ou CAPTCHA ele **para e pede take-over** — não tenta
contornar:

```bash
agentd_run '-screen 1 -prompt "Entre em https://github.com/login com o usuário andrebassi"'
# → tela 1: PRECISA DE VOCÊ — precisa de senha ou passkey
```

### Take-over: retomar e abandonar

```bash
task open                                       # veja a tela e faça o que falta
agentd_run '-resume -task task-1788... -note "senha digitada, pode seguir"'
agentd_run '-abandon -task task-1788...'        # desiste e libera a tela
```

### Telas: mais de um agente na mesma máquina

```bash
agent-status                    # o que cada tela está fazendo
screen-add 2                    # cria a tela 2, semeada com a sessão da 1
screen-remove 2                 # derruba (o perfil do navegador fica)
task screens                    # lista as telas ativas

# duas tarefas ao mesmo tempo, telas diferentes
agentd_run '-screen 1 -prompt "pesquise A"' &
agentd_run '-screen 2 -prompt "pesquise B"' &
```

Teto de **4 tarefas simultâneas** na máquina; a quinta recebe `429`.

### Pela porta HTTP

```bash
TOKEN=$(cat /workspace/agent/api-token)

# criar
curl -sS -X POST http://127.0.0.1:8787/tasks \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"prompt":"@digitalocean liste meus droplets","screen":1}'

# consultar (traz o estado E a resposta)
curl -sS http://127.0.0.1:8787/tasks/task-1788... -H "Authorization: Bearer $TOKEN"

# retomar depois do take-over
curl -sS -X POST http://127.0.0.1:8787/tasks/task-1788.../resume \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"note":"senha digitada"}'

# abandonar
curl -sS -X POST http://127.0.0.1:8787/tasks/task-1788.../abandon \
  -H "Authorization: Bearer $TOKEN"

# saúde (a única rota sem token)
curl -sS http://127.0.0.1:8787/health
```

Os códigos que importam: `201` criada · `409` **aquela tela** ocupada · `429` a
**máquina** cheia · `401` token errado · `413` corpo acima de 64 KB.

De fora, pelo túnel SSH — a porta nunca sai de `127.0.0.1`:

```bash
ssh -N -L 8787:127.0.0.1:8787 root@<maquina> &
curl -sS http://127.0.0.1:8787/health
```

### Catálogo: conectores e habilidades

```bash
agentd -catalog list                                  # o que está instalado
agentd -catalog install examples/connectors/gitlab.yaml
agentd -catalog secret gitlab-token                   # pede pelo stdin, sem eco
agentd -catalog remove gitlab
agentd -catalog skill-save deploy-check < /tmp/procedimento.md
agentd -catalog skill-remove deploy-check
```

O `list` diz, por conector, **se a credencial existe** — é a diferença entre
"não funciona" e "faltou um passo":

```
CONECTORES (2)

  @digitalocean — credencial configurada
     Consulta e opera recursos do DigitalOcean pela API: droplets, volumes ...
     · digitalocean.get_account
     · digitalocean.list_droplets
     · digitalocean.list_snapshots

  @gitlab — ⚠️  CREDENCIAL FALTANDO — agentd -catalog secret gitlab-token
     Trabalha com issues e merge requests do GitLab pela API, em vez de cli...
     · gitlab.create_issue
     · gitlab.list_issues

HABILIDADES (2)
  /estilo — Responda sempre comecando com a palavra CONFIRMADO.
  /web-search — Buscar qualquer coisa na internet
```

⚠️ `-catalog secret` **recusa entrada que não venha de terminal**. É de
propósito: `echo "$TOKEN" | agentd -catalog secret x` deixaria o segredo no
histórico do shell e em `ps`. Rode no terminal e digite.

### Diagnóstico

```bash
agentd -connector-probe https://api.exemplo.com/health   # alcançável?
agentd -vault-check                                       # o cofre ABRE?
agentd -notify-drain                                      # avisos pendentes (não consome)
agentd -notify-drain -webhook https://hooks.exemplo/xyz   # entrega e limpa

O que cada um responde:

```
$ agentd -vault-check
cofre legivel

$ curl -sS http://127.0.0.1:8787/health
{"status":"ok"}

$ agentd -notify-drain
37 aviso(s) pendente(s), sem destino configurado:
  tela 1 PRECISA DE VOCÊ: precisa de senha ou passkey — A página de login pede
    usuário e senha. Não envie a senha no chat.
  tela 4 PRECISA DE VOCÊ: um limite de segurança foi atingido — a ferramenta
    shell falhou 1 vez seguida com os mesmos argumentos: cat: /workspace/x: No
    such file or directory
  tela 5 PRECISA DE VOCÊ: um limite de segurança foi atingido — a tarefa já
    custou US$ 0.0050 em inferência (teto US$ 0.0005, somando as retomadas) e
    parou. Foram 2787 tokens de entrada e 26 de saída em 1 turnos.
  tela 3 falhou: processo interrompido; estado reconciliado no boot
```

⚠️ `-notify-drain` **sem `-webhook` não consome** a fila: dá para olhar quantas
vezes quiser. Com webhook, entrega e limpa — e só aí some.

**A fila só sai da máquina com um destino configurado** — sem ele o agente
enfileira o pedido de take-over e ninguém é avisado. O destino atual é o
**ntfy.sh**, com o tópico secreto guardado em `bassi/agent-computer/ntfy-url`:

```bash
task notify-setup     # grava /etc/agentd/notify.env e prova a entrega ponta a ponta
```

Dois formatos, escolhidos por `AGENT_WEBHOOK_FORMAT`:

| Formato | Corpo | Para quem |
|---|---|---|
| `raw` (padrão) | JSON com `task_id`, `screen`, `kind`, `reason`, `message` | endpoint escrito para este projeto |
| `ntfy` | o **texto** do aviso, com `Title`, `Priority` e `Tags` em cabeçalho | ntfy.sh, e qualquer coisa que renderize o corpo |

O formato importa mais do que parece: entregue como JSON, o aviso chega ao
celular como uma linha de JSON cru — legível para uma máquina e inútil para quem
é acordado por ele às três da manhã.

⚠️ **Prioridade alta só para o take-over.** É o único aviso que trava a tela
esperando gente; marcar tudo como urgente ensina quem recebe a ignorar, e aí o
que importa se perde no meio.

⚠️ O arquivo fica em `/etc/agentd/notify.env`, `root:root 0600`, **fora de
`/workspace`**: o destino dos avisos num diretório que o modelo alcança seria o
modelo escolhendo para onde vão os próprios pedidos de socorro — e apagar a
linha bastaria para ele trabalhar sem ninguém olhando.

⚠️ No ntfy quem sabe o tópico **lê e publica** nele. O tópico é a única
credencial que existe, por isso vive no `pass` e nunca aparece em log, em
argumento de comando ou na saída do script.

E `agent-status`, que é o retrato da máquina:

```
=== telas ===
  tela 1: openbox=active x11vnc=active novnc=active chrome=active | web 127.0.0.1:6081 CDP 127.0.0.1:9221
  tela 2: openbox=active x11vnc=active novnc=active chrome=active | web 127.0.0.1:6082 CDP 127.0.0.1:9222

=== estado duravel ===
  volume: VOLUME_MONTADO
  /dev/sda         20G  1.6G   18G   9% /workspace
```

task answers            # a RESPOSTA das últimas tarefas, não só o estado
task serve-status       # porta HTTP e timer de avisos
task serve-logs         # log do serviço
task health             # separa os 4 diagnósticos de máquina inalcançável
```

### Ciclo de vida da máquina

```bash
task up                    # cria (Ubuntu + cloud-init, o padrão)
AGENT_OS=nixos task up     # cria com NixOS declarativo
DROPLET_IMAGE=<id> task up # cria a partir de imagem pronta: pula ~15 min

task deploy                # compila e instala o agentd (gate de cobertura antes)
task nixos:rebuild         # aplica mudança de config em 46-69s, sem recriar
task restart               # reinicia os serviços
task update                # atualiza pacotes
task snapshot              # foto do VOLUME (o trabalho)
task image-snapshot        # foto do SISTEMA (a máquina)
task restore -- --latest   # restaura o volume
task destroy               # derruba o droplet; o volume sobrevive
task cost                  # quanto está custando agora
```

### O que fica registrado depois de cada tarefa

Nenhum dos quatro arquivos é escrito pelo modelo — todos são `agentd:agent 0640`
(ele lê, não escreve). Trechos reais desta máquina:

```
$ tail -3 /workspace/agent/progress.md
[…] tarefa=task-1788144095182313041 tela=3 estado=done turnos=1 resposta=tudo certo
[…] tarefa=task-1788144110161016609 tela=4 estado=blocked turnos=1 motivo=guardrail
    detalhe=a ferramenta shell falhou 1 vez seguida com os mesmos argumentos:
    cat: /workspace/nao-existe-guardrail.txt: No such file or directory
[…] tarefa=task-1788144159324010959 tela=5 estado=blocked turnos=1 motivo=guardrail
    detalhe=a tarefa já custou US$ 0.0050 em inferência (teto US$ 0.0005) e parou.

$ tail -2 /workspace/agent/activity.log
[…] tarefa=…226439714498 tela=3 iteracao=1 turnos=1 duracao=6.956s tokens=2808/8
    cache=512 custo=US$0.0049 parada=tool_calls ferramentas=ecoteste.eco
[…] tarefa=…226439714498 tela=3 iteracao=2 turnos=2 duracao=4.173s tokens=2909/101
    cache=2688 custo=US$0.0073 parada=stop ferramentas=nenhuma
```

Repare no `cache=`: da primeira para a segunda iteração ele salta de 512 para
2688 tokens. É o mesmo prompt sendo reaproveitado a **1/4 do preço** — e é por
isso que a ordem das ferramentas no prompt é estável.

E a redação aparecendo no registro, com o cabeçalho de um conector:

```
resposta=A operação eco devolveu exatamente isto:
{"headers":{"host":"postman-echo.com","x-segredo-de-teste":"[REDIGIDO]…
```

### Ver um guardrail disparar, de propósito

Os tetos são variáveis de ambiente, então dá para provocá-los sem esperar duas
horas nem gastar três dólares — é assim que a suíte 36 os exercita:

```bash
# custo: teto de meio centavo, qualquer tarefa estoura
AGENTD_MAX_COST_USD=0.0005 agentd -screen 5 -prompt "resuma o que é DNS"
# → blocked: a tarefa já custou US$ 0.0050 em inferência (teto US$ 0.0005,
#   somando as retomadas) e parou. Foram 2787 tokens de entrada e 26 de saída.

# laço de ferramenta: uma falha idêntica já basta
AGENTD_MAX_TOOL_FAILURES=1 agentd -screen 4 \
  -prompt "rode: cat /workspace/nao-existe.txt"
# → blocked: a ferramenta shell falhou 1 vez seguida com os mesmos argumentos

# turnos acumulados
AGENTD_MAX_TURNS=1 agentd -screen 3 -prompt "pesquise três coisas diferentes"

# e a máquina cheia — no serviço HTTP, que é quem conta as simultâneas
AGENTD_MAX_CONCURRENT_TASKS=1 …   # a segunda tarefa recebe 429
```

⚠️ As telas acima de 2 **não existem por padrão**: `screen-add 5` antes, senão o
pedido é recusado por tela inválida. A suíte 36 cria as que usa.

Valor inválido cai no padrão: teto desligado por engano é o defeito que eles
existem para evitar.

⚠️ **`blocked` não é `failed`.** A tela e o trabalho ficam; `-resume` continua de
onde parou. Se o guardrail encerrasse a tarefa, parar cedo custaria tudo — e a
primeira reação de quem usa seria desligá-lo.

### Limites, e como ajustá-los

Todos são variáveis de ambiente do serviço:

```bash
AGENTD_MAX_TURNS=180              # turnos acumulados por tarefa
AGENTD_MAX_TOOL_FAILURES=3        # falhas idênticas seguidas
AGENTD_MAX_COST_USD=3.00          # custo por tarefa, em dólares
AGENTD_MAX_CONCURRENT_TASKS=4     # tarefas simultâneas na máquina
```

Valor inválido cai no padrão — teto desligado por engano é o defeito que eles
existem para evitar.

### Verificação

```bash
task lint              # 5 gates de script, no Mac
task nixos:validate    # config NixOS: sintaxe, ASCII, sistema inteiro
task test:cov          # cobertura ≥90%, domínio 100%, com -race
task suites            # 6 suítes de máquina
task guardrails-test   # os detectores bloqueiam de verdade
task redaction-test    # o segredo some do histórico
task ssrf-test         # conector não alcança a rede interna
task functional        # os 3 que chamam o modelo
task hostile           # entrada malformada, degradação, concorrência
task examples          # recaptura as saídas deste receituário na máquina
```

⚠️ **Os exemplos acima não são de memória**: `task examples` roda cada comando
na máquina e imprime o que volta. Se um deles mudar de forma, a diferença
aparece aqui — exemplo em documentação envelhece calado.

## Guardrails: o que para o agente

Contenção do **comportamento em execução** — separada da de infraestrutura
(cofre, rebaixamento de usuário, firewall), que está em
[`SECURITY.md`](docs/SECURITY.md). O detalhe inteiro em
[`GUARDRAILS.md`](docs/GUARDRAILS.md).

Cinco detectores, todos em código, todos terminando em `blocked` + take-over —
a mesma máquina que o agente usa para pedir ajuda diante de uma senha:

| Detector | Limiar | Ajustável por |
|---|---|---|
| turnos acumulados por tarefa | 180 | `AGENTD_MAX_TURNS` |
| mesma ferramenta falhando com os mesmos argumentos | 3 | `AGENTD_MAX_TOOL_FAILURES` |
| custo acumulado em dólares | US$ 3,00 | `AGENTD_MAX_COST_USD` |
| fração do tempo da tarefa | 80% de 2 h | — |
| **tarefas simultâneas na máquina** | 4 | `AGENTD_MAX_CONCURRENT_TASKS` |

Mais quatro arquivos de memória em `/workspace/agent/`, `agentd:agent 0640` — o
modelo lê e **nunca escreve**:

```
guardrails.md   lições que ENTRAM NO PROMPT de toda tarefa nova
progress.md     desfecho de cada tarefa
activity.log    por iteração: ferramenta, duração, tokens, cache, custo
errors.log      falha de ferramenta com contagem de repetição
```

### A ideia veio do ralph; o mecanismo, não

Os quatro arquivos são do [ralph](https://github.com/iannuttall/ralph). Lendo o
código dele, quase nada é enforcement: **o único gate determinístico do sistema
inteiro é um `grep`** por `<promise>COMPLETE</promise>` no stdout — um sinal que
o próprio modelo decide emitir. E a documentação afirma que as lições são
*"injected into context at the start of each iteration"*; o código injeta o
**caminho** do arquivo e pede que o modelo o leia. Nenhuma linha lê o conteúdo.

Aqui a divisão é explícita: **detectar é código, conter é mudança de estado, e o
serviço lê o que escreveu.** Nada da contenção depende de o modelo cooperar.

Por que `blocked` e não `failed`: o guardrail **para** a tarefa, não a joga fora.
O trabalho e a tela ficam, e a pessoa decide se retoma. Se encerrasse, parar cedo
custaria tudo o que já foi feito — e a primeira reação seria desligá-lo.

## Dois sistemas, e os dois valem

A máquina pode nascer de dois jeitos. **Nenhum substitui o outro.**

```bash
task up                    # Ubuntu + cloud-init  (padrão)
AGENT_OS=nixos task up     # NixOS declarativo
```

| | Ubuntu | NixOS |
|---|---|---|
| como é descrito | `cloud-init/user-data.yaml`, 658 linhas | `nixos/host.nix` |
| como chega | cloud-init imperativo | instalação sobre o Ubuntu, no lugar |
| estado | 29 passos de `runcmd` em ordem | declaração |
| erro de sudoers | descarta o arquivo inteiro **em silêncio** | **não compila** |
| verificação antes de gastar droplet | YAML + ASCII | `task nixos-check` avalia o sistema **inteiro** |

O Ubuntu continua sendo o padrão **de propósito**: é o caminho que passou pelas
três suítes, e mantê-lo intacto é o que torna o NixOS uma escolha em vez de uma
aposta. Voltar custa uma variável, não um `git revert`.

### Por que o NixOS existe aqui

Três defeitos desta sessão foram da mesma classe — estado imperativo divergindo
da intenção **sem avisar**:

| Sintoma | Causa |
|---|---|
| `agent` sem sudo nenhum | erro de sintaxe no sudoers descartou o drop-in inteiro |
| toda tela em 409 permanente | `locks/` ficou com o dono antigo; o supervisor lê falha de trava como "tela ocupada" |
| proatividade quebrada em silêncio | `agentd-notify` ficou como `User=agent` depois da separação de usuários |

No caminho declarativo os três deixam de ser possíveis, e isso é **medido**, não
afirmado: `task nixos-check` com um `commands = "ALL"` malformado reprova com

```
error: A definition for option `security.sudo.extraRules."[...]".commands'
is not of type `list of (string or (submodule))'
```

— no Mac, antes de existir droplet.

### O que o `nixos-check` já pegou

Duas coisas que teriam custado uma reconstrução cada:

1. **`websockify` não existe no topo do nixpkgs** (é `python3Packages.websockify`).
   Seria uma tela sem noVNC, descoberta só ao abrir o navegador.
2. Com `users.mutableUsers = false` e sem chave de root declarada, o NixOS
   afirma: *"Neither the root account nor any wheel user has a password or SSH
   authorized key."* Seria um droplet **inalcançável**.

### Duas armadilhas de operação, medidas nesta troca

**A API do DigitalOcean é eventualmente consistente.** Um `destroy` seguido de
`create` encontra o droplet ainda listado e sai com *"já existe — nada a fazer"*,
**com `rc=0`**. O caminho inteiro parece ter dado certo e nenhuma máquina foi
criada. O `01-create.sh` passou a ler duas vezes, com 6 s de pausa.

**Não editar um script enquanto ele executa.** O bash relê o arquivo a partir do
deslocamento antigo, e uma edição no meio produz `syntax error near unexpected
token` numa linha que está sintaticamente correta em disco. Custou um
diagnóstico inteiro nesta sessão.

> 🛑 **`NIXOS_IMPORT`, nunca `NIXOS_CONFIG`.** O guard
> `[[ -e /etc/nixos/configuration.nix ]] && return 0` do instalador aborta a
> função inteira — pulando também `hardware-configuration.nix` e a configuração
> de **rede**. Pré-escrever a config faria a máquina subir sem rota.

## Arquitetura

O ponto que decide tudo: **o droplet é descartável, o volume é o computador.**

```
volume de bloco  20 GB          droplet  s-2vcpu-4gb
agent-computer-workspace        (substituível a qualquer momento)
        │                                  │
        └── /workspace  ────────────────── ├── /scratch      efêmero
             ├── browser/screen-1          ├── pacotes       efêmero
             ├── browser/screen-2          └── sistema       efêmero
             └── projects/
```

Sem essa separação o verbo **Update** da doc é impossível: não há como trocar a
imagem do computador preservando o trabalho se as duas coisas são o mesmo disco.

### Uma máquina, N telas

Como na doc, cada agente ganha uma tela na **mesma** máquina. Units systemd são
templates; `screen-add 2` cria a tela 2.

| Tela | VNC | web (noVNC) | CDP | perfil |
|---|---|---|---|---|
| 1 | 5901 | 6081 | 9221 | `/workspace/browser/screen-1` |
| 2 | 5902 | 6082 | 9222 | `/workspace/browser/screen-2` |
| N | 5900+N | 6080+N | 922N | `/workspace/browser/screen-N` |

⚠️ **Telas não são fronteira de segurança** — a doc diz isso explicitamente, e
vale aqui igual. Todas compartilham `/workspace`, as credenciais de linha de
comando e o mesmo `sudo`. Quem alcança uma alcança tudo.

Custo medido: **~500 MB de RAM por tela** (980 MB com uma, 1,5 GB com duas). Com
4 GB cabem ~5.

### Os três verbos da doc

| Verbo | Comando | Preserva | Descarta |
|---|---|---|---|
| **Update** | `task update` | `/workspace`, perfis, sessões | `/scratch`, pacote manual, sistema |
| **Reset** | `task reset` | nada além do snapshot | trabalho posterior ao snapshot |
| **Recover** | `task update` | idem Update | idem Update |

`Recover` não ganhou comando próprio: com o estado no volume, recuperar um
droplet inalcançável é exatamente reconstruí-lo, que é o que `update` faz.

## Acesso — nada é publicado

`ufw` deixa passar só a 22. VNC, noVNC e CDP escutam apenas em loopback dentro
do droplet. O caminho é um túnel SSH:

```bash
task open      # túnel + tela no navegador local
```

Consequência assumida: **quem tem a chave SSH tem o desktop**, sem segunda
senha. Para um lab de duas pessoas é a troca certa — senha VNC exposta seria
pior.

### Pela malha Tailscale

O computador também entra na malha, como `agent-computer`. Os scripts **preferem
a malha** e caem para o IP público se ela não estiver disponível:

```
$ task open
rota: malha Tailscale (100.70.182.102)
```

O ganho não é estético: **o IP público muda a cada rebuild do droplet**, e o
endereço da malha não. Cinco reconstruções num dia produziram cinco IPs
diferentes, cada uma invalidando comando anotado, entrada de `known_hosts` e
túnel aberto. Na malha o acesso também passa a ser governado pela ACL do tailnet,
o que permite compartilhar o nó sem entregar a chave SSH da máquina.

A reserva é silenciosa e **testada nos três cenários**: malha no ar, nó offline
nela, e cliente ausente da máquina. Uma malha caída não pode impedir o acesso a
um computador que está de pé.

Ligar: `./scripts/15-tailscale-up.sh` imprime a URL de autorização. O script
**não liga `--ssh`** de propósito — isso abriria acesso ao shell governado pela
ACL do tailnet, que é decisão de segurança separada de "entrar na malha".

## Comandos

```bash
task check        # binários, token, chave SSH, latência — antes de gastar
task up           # cria volume (se faltar), droplet, espera e valida
task open         # túnel SSH + tela no navegador
task ssh          # shell como usuário agent
task screens      # telas ativas, estado durável, recursos
task validate     # as 10 seções
task snapshot     # snapshot do volume durável
task update       # rebuild preservando o durável
task reset        # volta ao snapshot, descarta trabalho recente
task destroy      # destrói o droplet (o volume fica)
```

Para criar tela: `task ssh`, depois `screen-add 2`.

### Dois agentes, com pontos fortes diferentes

| | Ferramentas | Bom para |
|---|---|---|
| **agentd** (Grok) | shell, navegador, conectores, habilidades, take-over | navegar, chamar API, tarefa com barreira sensível |
| **Claude Code** | edição de arquivo, git, busca, subagentes | mexer em código e em muitos arquivos |

O Claude Code roda com a chave em `/workspace/agent/anthropic.env` (permissão
`0600`), carregada por `set -a; . /workspace/agent/anthropic.env; set +a`.

⚠️ **A chave fica ao alcance de qualquer agente da máquina** — o computador é
compartilhado por construção, e é a mesma consequência que a documentação avisa
sobre credenciais de linha de comando. Não coloque ali chave que outro agente
não deva usar.

### O agente

Dentro da máquina, `/usr/local/bin/agentd`:

```bash
agentd -screen 1 -prompt "a tarefa"           # roda uma tarefa
agentd -resume  -task <id> -note "resolvi"    # devolve o controle após take-over
agentd -abandon -task <id>                    # desiste e libera a tela

agentd -prompt "@gitlab liste as issues do projeto 12345"   # conector
agentd -prompt "@github siga /release e publique"           # conector + habilidade
```

| Sintaxe | O que faz | Onde vive |
|---|---|---|
| `@nome` | anexa um conector à tarefa | `/workspace/agent/connectors/installed/` |
| `/nome` | injeta uma habilidade salva | `/workspace/agent/skills/<nome>.md` |

Manifesto de conector em **JSON ou YAML**. Catálogo de exemplos prontos e
testados em [`examples/`](examples/README.md) — DigitalOcean, Cloudflare, GitLab
e GitHub, mais duas habilidades. Esse arquivo também lista **o que não dá para
conectar e por quê** (GLPI, AWS, Google), para poupar a tentativa.

⚠️ **Conectores são de conta**: instalar um o torna disponível a todas as telas, e
a credencial fica ao alcance de qualquer agente da máquina.

⚠️ **Tarefa bloqueada trava a tela até alguém decidir.** O estado é durável, então
ela sobrevive a reboot, destroy e rebuild. `-abandon` é a saída, funciona sem a
chave da API, e a mensagem de tela ocupada aponta para ele. Descoberto no teste
integrado: uma tarefa do dia anterior derrubou tudo em cascata.

## Custo — medido em 2026-08-29

| Item | Valor |
|---|---|
| Droplet `s-2vcpu-4gb`, nyc3 | US$ 24,00/mês (US$ 0,03571/h) |
| Volume durável 20 GB | US$ 2,00/mês |
| Snapshot do volume | US$ 0,06/GB/mês |
| Imagem do sistema (9,58 GiB) | US$ 0,57/mês — **opcional**, corta ~15 min de cada recriação |
| **Total com droplet ligado** | **US$ 26,00/mês** |
| **Só o estado, droplet destruído** | **US$ 2,00/mês** |
| Cobrança | por segundo, mínimo 60 s |
| Bandwidth incluso | 4 TB |

O padrão barato ficou melhor com o volume: `task destroy` derruba o droplet e o
trabalho **continua no volume** por US$ 2/mês. `task up` traz tudo de volta.

## Fidelidade à doc

| Item da doc | Aqui |
|---|---|
| computador persistente por conta | ✅ volume durável, droplet descartável |
| uma tela por agente, mesma máquina | ✅ templates systemd, `screen-add` |
| telas não são fronteira de segurança | ✅ e documentado como tal |
| `/workspace` compartilhado | ✅ no volume |
| durável × descartável | ✅ `/workspace` vs `/scratch` |
| Update / Recover / Reset | ✅ com semânticas distintas |
| ver o trabalho acontecendo | ✅ noVNC pelo túnel |
| assumir o controle | ✅ mesma tela, teclado e mouse |
| sessões de navegador persistem | ✅ perfil no volume |
| **cookies compartilhados entre bots** | ❌ **divergência**, ver abaixo |
| conectores anexados com `@` | ✅ manifesto JSON ou YAML vira ferramenta |
| habilidades salvas com `/` | ✅ em `/workspace/agent/skills` |
| conectores de conta, não de agente | ✅ catálogo no volume durável |
| tela de catálogo (`Settings → Plugins`) | ❌ é interface, não infraestrutura |
| secret request como fluxo de tela | ⚠️ o tipo e a garantia existem; falta a tela |

### Sessão entre telas: semeadura e propagação, não compartilhamento

A documentação diz que logar num site por um agente deixa a sessão disponível
para os outros. **Compartilhamento literal é impossível**: o Chrome mantém um
`SingletonLock` no perfil e recusa um segundo processo no mesmo diretório —
verificado na máquina, a trava aponta para `<host>-<pid>`.

O que existe são dois mecanismos que entregam o efeito prático:

| Mecanismo | Quando | O que faz |
|---|---|---|
| **semeadura** | `screen-add N` | a tela nova nasce com cópia do perfil da tela 1 |
| **propagação** | `session-sync 1 2` | leva a sessão de uma tela para outra, depois |

Isso funciona por um detalhe que não é óbvio: o Chrome sobe com
`--password-store=basic`, e nesse modo os cookies são cifrados com **chave
fixa**, não com o chaveiro do usuário. Com o chaveiro, a cópia produziria um
perfil cujos cookies não descriptografam — e o sintoma seria "deslogado", sem
erro nenhum.

**O que continua diferente da documentação:** não há sincronização contínua. Um
login feito agora na tela 1 não aparece sozinho na tela 2; é preciso rodar
`session-sync`. Sincronizar de verdade exigiria dois processos escrevendo no
mesmo SQLite de cookies, que corrompe.

O `session-sync` para o Chrome do **destino** antes de copiar, guarda o perfil
anterior em `screen-N.anterior`, e **reverte sozinho** se o navegador não
religar.

### Detalhe descartado: o que não deu certo

Na doc, logar num site por um bot deixa a sessão disponível para os outros. Aqui
cada tela tem perfil próprio, porque o Chrome trava o `user-data-dir` e recusa um
segundo processo no mesmo diretório.

Contornos possíveis, nenhum implementado: um Chrome só com janelas em displays
diferentes (o Chrome não faz), sincronizar o banco de cookies entre perfis
(corre risco de corromper), ou um proxy com jar compartilhado (não cobre
`localStorage` nem sessão de app).

## Armadilhas já pagas

### O DigitalOcean corrompe user-data não-ASCII, e o cloud-init recusa calado

A mais cara: **três droplets descartados** antes de achar.

Um `acessível` sai do disco como `C3 AD` e chega no droplet como `C3 83 C2 AD` —
dupla codificação UTF-8 no caminho API → ConfigDrive. O `C2 80` que o travessão
duplo-codificado gera é caractere de controle C1, e o cloud-init recusa o
**arquivo inteiro**:

```
Failed loading yaml blob. unacceptable character #x0080 ... position 450
```

Três coisas escondem isso:

1. **A recusa é silenciosa.** O droplet reporta `status: done`, sobe, aceita SSH
   — e não instalou nada. Sem usuário `agent`, sem `/workspace`, sem pacote.
2. **O motivo sai no stderr.** `cloud-init status --long 2>/dev/null` esconde
   justamente os `recoverable_errors`.
3. **Não é culpa do cliente.** Reproduzido em `doctl` 1.145.0, 1.167.0 e na API
   REST direta com `jq`, com o payload provado byte-idêntico na origem.

Correção: o `user-data.yaml` é **ASCII puro** com aviso no topo, e o
`01-create.sh` reprova qualquer byte não-ASCII antes de criar o droplet. Só os
scripts locais levam acento — lá o problema não existe.

### Volume anexado depois é pior que volume nenhum

Se o volume for anexado após a criação, o cloud-init roda antes de o device
existir, cria `/workspace` no disco local e **tudo funciona**. O estado some no
primeiro update, sem nenhum sintoma antes disso. Por isso o volume vai no corpo
da criação e há espera explícita pelo device, com `nofail` no `fstab` para um
boot sem volume não cair em emergency shell.

### `cmd | tee` engole o código de saída

O primeiro `task up` saiu **rc=0 sem ter criado droplet nenhum**. `set: [pipefail]`
no Taskfile resolve; canário confirma rc=201 com e rc=0 sem.

### O check de latência mentia

`speedtest-nyc3.digitalocean.com` foi desativado e nem resolve em DNS; o `curl`
falhava calado e o gate reportava `0ms`. Agora é `ping` contra droplet real da
região — sem sonda, diz que não mediu em vez de inventar.

### Espera que não distinguia estados

A primeira versão do `02-wait-ready.sh` rotulava "aguardando SSH responder"
quando o SSH já autenticava e só o arquivo-marca faltava — porque `cat` de
arquivo inexistente devolve rc=1. Custou 12 minutos de diagnóstico na direção
errada. Agora separa três estados e aborta na hora se o YAML foi recusado.

### `runcmd` não aborta, e o cloud-init reporta sucesso mesmo assim

Comando que falha dentro de `runcmd` **não interrompe o cloud-init**. Ele segue
para o próximo, escreve `READY` no fim e reporta `status: done`. Um
`npm install -g` morto por rede deixaria o computador sem agente de código com
todos os sinais de saúde verdes.

O que torna isso pior que uma falha ruidosa: a ausência só apareceria na
primeira tarefa que delegasse, como *"o agente de código não está configurado"*
— mensagem que manda procurar no **arquivo de credencial**, que está lá e
correto. O diagnóstico começa no lugar errado.

Fechado com retry, marcador em `/var/lib/agent-computer-code-agent` e uma seção
do `task validate` que separa binário ausente de credencial ausente.

**Erro de raciocínio junto:** a pendência original dizia *"o Claude Code está em
`/usr/bin`, some no update"*. A dedução sobre o filesystem estava certa e a
conclusão errada — o `cloud-init` o reinstala em todo rebuild, e ele estava lá
desde o primeiro commit. Ler o arquivo antes teria custado dez segundos.

### Outras

- **`lib.sh` não tem `set -e`.** É sourceado: o flag vazaria e mataria o script
  chamador no primeiro `grep` sem resultado, sem mensagem.
- **`ssh_authorized_keys: []` reprova no schema** (`[] is too short`). A chave é
  copiada de `/root/.ssh` pelo `runcmd`.
- **Snapshot desliga antes.** A quente pode capturar o disco a meio de escrita.

## Medido nesta máquina, em 2026-08-29

| | |
|---|---|
| Latência até nyc3 | 114 ms (ping contra droplet real) |
| Boot + cloud-init completo | ~4 min |
| Update completo (rebuild) | ~6 min |
| RAM com 1 tela | 980 MB de 3915 (25%) |
| RAM com 2 telas | 1,5 GB de 3915 (~500 MB por tela) |
| Perfil do navegador após uso leve | 286 MB |
| Chrome | 152.0.7977.64 |
| Reboot até serviços ativos | ~70 s |

## Pendências

- [x] ~~Nenhum agente roda~~ — o agente Grok roda, validado ponta a ponta em 30/08
- [x] ~~Coleta do secret request~~ — `agentd -catalog secret <ref>`, com eco desligado
- [x] ~~Catálogo de conectores~~ — `agentd -catalog list|install|remove|skill-save`
- [x] ~~Detecção de computador inalcançável~~ — `task health`, que separa os quatro
      diagnósticos e indica a recuperação **menos destrutiva primeiro**
- [~] **Sessão entre telas** — semeadura e propagação implementadas e testadas na
      máquina; sincronização contínua continua impossível (o Chrome trava o perfil)
- [x] ~~Tailscale autenticado~~ — na malha como `agent-computer`; os scripts
      preferem a malha, com o IP público como reserva testada
- [x] ~~KasmVNC~~ — **avaliado e medido**: −82% de memória (424 MB → 74 MB por tela)
      e resolução dinâmica que o Xvfb recusa. Decisão registrada: **não trocar agora**,
      porque nada está limitando; gatilho é precisar de mais de três telas

- [x] ~~O agente não pilota o navegador~~ — **fechado**. Seis ferramentas falam
      CDP com `127.0.0.1:922N`: `browser_navigate`, `browser_read`,
      `browser_links`, `browser_click`, `browser_fill` e `browser_screenshot`.

      Provado contra sites reais: o agente abriu `example.com`, leu a página,
      clicou em "Learn more" e reportou o destino. E diante de `github.com/login`
      ele navegou, leu, **preencheu o usuário** e parou pedindo take-over — que é
      a cláusula da documentação funcionando por inteiro.

- [x] ~~O laço não tinha contenção nenhuma~~ — **fechado**. Quatro detectores em
      código (turnos, ferramenta em laço, custo em dólares, tempo de parede),
      quatro arquivos de memória que o modelo lê e nunca escreve, e a lição
      aprendida entrando no prompt da tarefa seguinte.

      Junto vieram quatro buracos que já existiam: `ToolResult.Failed` era
      escrito por toda ferramenta e **lido por nenhuma**; o contador de turnos
      zerava a cada retomada; `StopReason` nunca era lido, então resposta
      truncada virava `done` **com sucesso**; e `Resume` persistia `running`
      antes de tomar a trava, transformando tarefa `blocked` em `failed` quando
      a tela estava ocupada.

      Detalhe em [`GUARDRAILS.md`](docs/GUARDRAILS.md); prova em
      `task guardrails-test`, 12 seções na máquina real.

- [x] ~~Orquestrar Grok e Claude Code~~ — ferramenta `delegate_to_code`,
      **provada com a tarefa mista**: o Grok leu `stargazers_count = 136822` da
      API do GitHub pelo navegador e delegou; o Claude Code escreveu três
      arquivos Python e os 4 testes passaram, verificados de fora por SSH.
      Ver §5.6 e o segundo caso real.

- [x] ~~O Claude Code não sobrevive ao `update`~~ — **a pendência estava errada, e
      a correção verdadeira é outra.** Ele já era instalado pelo `cloud-init`
      desde o primeiro commit (Node 22 + `npm install -g`), então todo rebuild o
      reinstala. O que faltava era **verificação**: `runcmd` não aborta quando um
      comando falha, e um `npm` morto por rede deixaria o droplet sem agente de
      código com o cloud-init reportando sucesso. Corrigido com retry, marcador
      em `/var/lib/agent-computer-code-agent` e a seção 10 do `task validate`.

- [x] ~~CloakBrowser~~ — **avaliado**: Chromium com 73 patches em C++, reCAPTCHA
      v3 em 0,9. Decisão registrada: **não entra** — contradiz a regra nº 1 do
      agente (parar e pedir take-over) e a camada grátis dá 1 sessão concorrente,
      contra o modelo de N telas. Gatilho é o objetivo virar coleta em escala

### A pendência que não é técnica

O projeto está completo e **sem uso definido**. Reproduz o modelo, tem 35 cláusulas
atendidas e passa em quatro suítes — mas ninguém o usa para nada real, e custa
US$ 2/mês parado.

O passo que destravaria valor é escolher **uma tarefa repetitiva de verdade** e ver
se ele a executa melhor que à mão. Sem isso, permanece um estudo bem-feito.

---

# Como usar, com um caso real do começo ao fim


Este documento não é teoria: é a transcrição de uma tarefa que rodou de verdade
em 30/08/2026, com os comandos exatos, o que o agente fez e o que ele produziu.

Para a arquitetura e as decisões, ver a seção de arquitetura.

---

### O caso: o agente audita a própria infraestrutura

**O que se quer:** saber quanto a conta do DigitalOcean está custando e o que
existe nela, sem abrir o painel nem rodar comando à mão.

**Por que serve de exemplo:** é uma tarefa real, com credencial real, que o
agente não teria como cumprir sozinho — ele precisa de uma ferramenta que fale
com a API. E o resultado é conferível: o número ou bate com a fatura, ou não.

#### Passo 1 — subir o computador

```bash
cd ~/works/labs/agent-computer
task up
```

Cria o droplet, reanexa o volume durável e espera o `cloud-init`. Leva ~5 min. O
que estava em `/workspace` de antes volta intacto: perfil do navegador, tarefas,
conectores e habilidades.

#### Passo 2 — instalar o conector

O agente só alcança um serviço externo se houver um conector para ele. Instalar
é copiar o manifesto e gravar a credencial:

```bash
## manifesto
scp examples/connectors/digitalocean.yaml agent@<host>:/tmp/do.yaml
ssh agent@<host> '/usr/local/bin/agentd -catalog install /tmp/do.yaml'

## credencial, pelo stdin — NUNCA em linha de comando, onde `ps` a exporia
ssh agent@<host> 'install -m 600 /dev/stdin /workspace/agent/connectors/secrets/digitalocean-token' \
  <<< "$(pass show bassi/digitalocean/api-token)"
```

Conferindo:

```bash
ssh agent@<host> '/usr/local/bin/agentd -catalog list'
```

```
CONECTORES (2)

  @digitalocean — credencial configurada
     Consulta e opera recursos do DigitalOcean pela API: droplets, volumes ...
     · digitalocean.get_account
     · digitalocean.get_droplet
     · digitalocean.list_droplets
     · digitalocean.list_snapshots
     · digitalocean.list_volumes
```

⚠️ Se a credencial faltar, a listagem grita: `CREDENCIAL FALTANDO`. Isso evita o
modo de falha mais comum — conector instalado, credencial esquecida, e a tarefa
falhando na primeira chamada com um erro que parece vir da API.

#### Passo 3 — abrir a tela, para assistir

```bash
task open
```

```
rota: malha Tailscale (100.70.182.102)
✅ tunel no ar
   tela : http://127.0.0.1:6081/vnc.html?autoconnect=true&resize=scale
```

Abra a URL no navegador. Você vê o desktop do agente ao vivo — e pode assumir o
teclado e o mouse a qualquer momento.

#### Passo 4 — dar a tarefa

```bash
ssh agent@<host> "XAI_API_KEY='$(pass show bassi/xai/apikey)' \
  /usr/local/bin/agentd -screen 1 -task demo-audit \
  -prompt '@digitalocean Faca uma auditoria da conta: liste droplets, volumes e
   snapshots. Calcule o custo mensal somando US\$ 24 por droplet de 4GB,
   US\$ 0,10 por GB de volume e US\$ 0,06 por GB de snapshot. Grave o relatorio
   em /workspace/projects/infra-audit.md com uma tabela e o total. Confira que
   o arquivo existe.'"
```

O `@digitalocean` no começo é o que anexa o conector. Sem ele, o agente teria só
`shell` e `request_takeover`.

Saída:

```
conectores anexados: digitalocean (5 ferramentas)
tarefa demo-audit na tela 1
estado final: tela 1: concluída
```

#### Passo 5 — ver o que ele fez

```bash
ssh agent@<host> 'python3 /tmp/show-task-steps.py \
  /workspace/agent/conversations/demo-audit.json'
```

```
  PEDIDO: Faca uma auditoria da conta: liste droplets, volumes e snapshots...

  1. digitalocean.list_droplets({})
  2. digitalocean.list_volumes({})
  3. digitalocean.list_snapshots({})
  4. digitalocean.get_account({})
  5. shell({"command":"mkdir -p /workspace/projects && cat > /workspace/proj)

  CONCLUSAO: Auditoria concluída. O relatório está em
  /workspace/projects/infra-audit.md (arquivo confirmado: 74 linhas).

  ferramentas usadas: 5
```

**Cinco chamadas**: quatro no conector, uma no shell para gravar. E ele conferiu
o próprio trabalho no fim, contando as linhas do arquivo — a instrução de sistema
manda checar o resultado de cada passo.

#### Passo 6 — o resultado

```bash
ssh agent@<host> 'cat /workspace/projects/infra-audit.md'
```

Saiu um relatório de 74 linhas com três tabelas — droplets, volumes e snapshots —
mais um resumo de custo. Trecho:

```markdown
### Resumo de custo mensal

| Recurso | Quantidade | Base de cálculo | Custo (US$/mês) |
|---|---|---|---|
| Droplets 4 GB | 1 | 1 × US$ 24,00 | 24,00 |
| Volumes | 20 GB | 20 × US$ 0,10 | 2,00 |
| Snapshots | 0,7772 GB | 0,7772 × US$ 0,06 | 0,046632 |
| **Total** | | | **US$ 26,046632** |

Notas:
- O droplet `rustdesk-relay` (1 GB) não entra no critério de US$ 24.
- Os dois snapshots são de volume; o tamanho já está consolidado (não zerado).
```

**US$ 26,05/mês** — e o número bate com a realidade da conta.

O que vale reparar: **ele notou coisas que ninguém pediu.** Que o
`rustdesk-relay` não entra no critério por ter 1 GB, e que os snapshots já
estavam consolidados. Nenhuma das duas estava no pedido.

#### Passo 7 — derrubar

```bash
task snapshot   # guarda o estado
task destroy    # US$ 26/mês → US$ 2/mês
```

O trabalho fica no volume. `task up` traz tudo de volta.

---

### Quando ele para e chama você

O mesmo agente, diante de uma barreira sensível:

```bash
agentd -prompt 'Entre no painel https://painel.exemplo.com com o usuario admin.
                A pagina pede usuario e senha.'
```

```
estado final: tela 1: PRECISA DE VOCÊ — precisa de senha ou passkey

A tarefa espera você. Abra a tela, resolva o passo e rode:
  agentd -resume -task <id>
```

Ele **para**. Não tenta adivinhar, não tenta contornar. A tela mostra o pedido,
a tarefa fica em `blocked`, e a tela recusa outras tarefas até alguém decidir:

```bash
agentd -resume  -task <id> -note "senha digitada"   # continua de onde parou
agentd -abandon -task <id>                          # desiste e libera a tela
```

⚠️ **A tarefa bloqueada sobrevive a reboot, destroy e rebuild** — o estado é
durável de propósito. Ela vai continuar travando aquela tela até você resolver
ou abandonar. Foi assim que um teste inteiro falhou em cascata: uma tarefa do dia
anterior segurava a tela 1.

---

### Reaproveitando um procedimento: habilidades

Quando o mesmo procedimento se repete, ele vira uma habilidade em vez de ser
recolado a cada vez:

```bash
agentd -catalog skill-save release ./meu-procedimento.md
agentd -prompt "@github siga /release e publique"
```

O conteúdo entra no texto da tarefa, delimitado:

```
publique a versão nova

--- habilidade salva: release ---
1. rode `task test:cov` e confirme o gate verde
2. crie a tag assinada
--- fim de release ---
```

A delimitação não é enfeite: sem ela, um procedimento longo se mistura ao pedido
e o modelo passa a tratar o procedimento como o objetivo.

⚠️ Limite de 8 KB por habilidade, porque o conteúdo entra no prompt **a cada
iteração** da tarefa, não uma vez.

---

### Outros casos que a mesma mecânica atende

Nenhum destes foi construído — são o que os conectores existentes já permitem:

| Tarefa | Conector | O que mudaria |
|---|---|---|
| "há droplet ligado sem uso há dias?" | `@digitalocean` | acha desperdício sem abrir o painel |
| "que registros DNS apontam para IP que não existe mais?" | `@cloudflare` | cruza zonas com droplets |
| "resuma as issues abertas do projeto" | `@gitlab` | sem clicar por página |
| "confira se o token do Cloudflare ainda vale" | `@cloudflare` | `verify_token` antes de culpar a API |

O padrão: **tarefa que exige cruzar dados de mais de uma fonte e escrever uma
conclusão.** Consulta simples não compensa — `doctl` responde mais rápido e de
graça. O agente ganha quando há julgamento no meio.

---

### Os limites, para não descobrir na hora errada

| Limite | Consequência |
|---|---|
| sem paginação automática | uma listagem grande volta truncada, e a API raramente avisa |
| resposta de API cortada em 8 KB | idem |
| sem upload de arquivo por conector | |
| teto de 60 iterações por tarefa | tarefa longa demais para no meio, com `ErrMaxIterations` |
| uma tarefa por tela | por decisão, e a tela fica travada até resolver ou abandonar |
| cookies não são compartilhados entre telas | divergência conhecida da doc — ver §10 da arquitetura |

> **O que mudou desde esta demonstração.** Ela foi escrita quando o agente
> **não dirigia o navegador**: o Chrome estava lá e o CDP aberto, mas nada os
> usava. As ferramentas `browser_*` fecharam essa lacuna, e é sobre elas que o
> segundo caso, logo abaixo, se apoia.

---

### Custo medido desta demonstração

| | |
|---|---|
| Tarefa de auditoria | 5 chamadas de ferramenta, ~40 s |
| Tokens | ~700 de entrada por iteração |
| Droplet ligado | US$ 24,00/mês (US$ 0,036/h) |
| Volume, com o droplet destruído | US$ 2,00/mês |

Rodar a auditoria custou frações de centavo em inferência. O caro é deixar o
droplet ligado sem usar — daí `task snapshot && task destroy` ao terminar.

---

### Segundo caso real: a tarefa mista — web + código

Rodou em **30/08/2026**. É o caso que justifica a ferramenta `delegate_to_code`,
porque **nenhum dos dois agentes o cumpre sozinho**: o Claude Code não enxerga a
página, e o Grok escreveria o código a `sed`.

**O que se quer:** ler um número que só existe numa página web e produzir código
testado que use aquele número.

#### O comando

```bash
task delegation-test        # scripts/17-delegation-test.sh
```

A tarefa, como chegou ao agente (uma linha só, é assim que ele recebe):

> Abra no navegador `https://api.github.com/repos/golang/go` e leia o valor do
> campo `stargazers_count`. Depois use `delegate_to_code` para pedir ao agente de
> código: crie em `/workspace/projects/star-count` um módulo Python `formatter.py`
> com a função `format_count(n)` que devolve o número com separador de milhar por
> ponto (exemplo: 1234567 vira 1.234.567), um `main.py` que imprime `format_count`
> do valor real que você leu, e `test_formatter.py` com `unittest` cobrindo zero,
> três dígitos, quatro dígitos e sete dígitos. Critério de pronto:
> `python3 -m unittest discover` passar dentro do diretório.

#### O que aconteceu, na ordem

```
Grok, tela 2
  ├─ browser_navigate  https://api.github.com/repos/golang/go
  ├─ browser_read      lê stargazers_count = 136822
  │
  └─ delegate_to_code  "…com o valor 136822, crie formatter.py, main.py e teste…"
          │
          └─ Claude Code em /workspace/projects/star-count
                ├─ escreve formatter.py, main.py, test_formatter.py
                ├─ roda python3 -m unittest discover
                └─ relatório de volta ──▶ Grok
  
estado final: tela 2: concluída
```

#### O que ele produziu

```python
# formatter.py
"""Formatação de números com separador de milhar."""


def format_count(n):
    """Devolve n com ponto como separador de milhar (ex.: 1234567 -> '1.234.567')."""
    return f"{n:,}".replace(",", ".")
```

```python
# main.py
from formatter import format_count

# stargazers_count de https://api.github.com/repos/golang/go
STARGAZERS_COUNT = 136822
```

#### A verificação, feita de fora

O script **não acredita no relatório do agente** — roda o critério de pronto por
conta própria, por SSH. Acreditar no relatório seria acreditar na palavra do
próprio testado:

```
--- unittest ---
test_quatro_digitos ... ok
test_sete_digitos   ... ok
test_tres_digitos   ... ok
test_zero           ... ok

Ran 4 tests in 0.000s
OK

=== 4/4 o programa imprime o numero lido da web? ===
136.822
rc=0
```

`136.822` é o número que estava na página naquele momento, formatado pelo código
que o outro agente escreveu. É a prova de que a informação atravessou a fronteira
entre os dois.

#### Quatro coisas que este caso ensinou

1. **O script apagou o diretório antes de começar.** Sem isso, um resultado de
   uma corrida anterior faria o teste passar sem o agente ter feito nada — o modo
   de falha mais silencioso que existe num teste de agente.

2. **A primeira corrida saiu com `rc=0` tendo falhado.** O agente morreu em
   `XAI_API_KEY não está no ambiente`, e o `| tee` da minha invocação devolveu o
   código do `tee`. É a mesma armadilha que o `Taskfile` já documenta em
   `set: [pipefail]` — e ela reapareceu num script novo, fora do Taskfile.

3. **O Claude Code escreveu os testes em português** (`test_quatro_digitos`).
   Ele não herda a convenção de idioma deste repositório, porque **não vê a
   conversa nem o `CLAUDE.md` de quem delegou**. Se o nome importar, a exigência
   vai *dentro* do texto delegado.

4. **Não havia Go na máquina.** A primeira versão do teste pedia um módulo Go e
   teria falhado no critério de pronto por falta de toolchain, não por culpa do
   agente. `unittest` é biblioteca padrão do Python e sobrevive ao rebuild sem
   entrar na lista de pacotes do `cloud-init`.

#### Custo desta demonstração

| | |
|---|---|
| Tarefa inteira, ponta a ponta | ~3 min |
| Chamadas de ferramenta do Grok | 3 (navegar, ler, delegar) |
| Inferências | 2 modelos, uma passada cada |

---

# Arquitetura ponta a ponta


> Documento técnico e didático. Explica **o que** foi construído, **por que** cada
> peça existe, e **como** cada cláusula da documentação do Grok Bot virou código
> executável e testado.
>
> Fonte que estamos reproduzindo:
> <https://docs.x.ai/grok-bot/computer-and-apps> (atualizada em 11/08/2026).
> Auditoria cláusula por cláusula, com placar: a auditoria de fidelidade.

---

### 1. O que este projeto é, em uma frase

Um **computador em nuvem persistente com tela própria, dirigido por um agente
autônomo**, montado em infraestrutura própria — reproduzindo o modelo que a xAI
descreve para o Grok Bot, que é produto fechado e não tem API nem pacote
instalável.

Não é um clone do Grok Bot. É a mesma **arquitetura**, construída do zero, com as
mesmas garantias, e com as divergências registradas onde elas existem.

---

### 2. O modelo que estamos reproduzindo

Antes de ver o código, vale entender o desenho que a documentação descreve,
porque quase toda decisão daqui decorre dele.

#### 2.1 A ideia central: o computador não é seu laptop

Um agente que trabalha no seu laptop para quando você fecha a tampa. O modelo do
Grok Bot separa as duas coisas:

```
   Seu Mac                          O computador do agente
   ┌──────────────┐                 ┌────────────────────────┐
   │ você olha    │ ── observa ───▶ │ navegador, shell,       │
   │ e assume o   │                 │ arquivos, ferramentas   │
   │ controle     │ ◀── pede ────── │ trabalhando sozinho     │
   └──────────────┘   ajuda         └────────────────────────┘
        fecha a tampa                    continua trabalhando
```

Fechar o laptop não interrompe nada. Você reabre e o trabalho avançou.

#### 2.2 Um computador por conta, N telas

Este é o ponto que mais gente lê errado. **Não é um computador por agente.**

```
                    UMA conta
                        │
              UM computador compartilhado
                        │
        ┌───────────────┼───────────────┐
     tela 1          tela 2          tela 3
    agente A        agente B        agente C
        │               │               │
        └───────────────┴───────────────┘
                        │
              /workspace COMPARTILHADO
              credenciais COMPARTILHADAS
              cookies COMPARTILHADOS
```

A documentação é explícita: as telas são **superfícies de trabalho separadas, não
fronteiras de segurança**. Se o agente A gravar uma credencial em `/workspace`, o
agente B a alcança. Isso não é defeito do desenho — é o desenho, e vem com o aviso
de não colocar ali segredo que outro agente não deva usar.

#### 2.3 O que sobrevive e o que não

A documentação separa dois mundos:

| Durável (sobrevive a update e recovery) | Descartável (some) |
|---|---|
| `/workspace` | diretórios temporários |
| estado do navegador, sessões logadas | pacotes instalados à mão |
| | estado de aplicação não salvo |

Essa fronteira é a base dos três verbos de manutenção. Sem ela declarada, tudo
vira igualmente frágil ou igualmente permanente — e nos dois casos a manutenção
fica impossível.

#### 2.4 O handoff humano

Quando o agente esbarra em senha, verificação em duas etapas, CAPTCHA, cobrança
ou um site que exige uma pessoa, ele **para e chama você**. Você resolve só aquele
passo e devolve o controle.

O que a documentação proíbe é o oposto: tentar contornar. Um agente que tenta
adivinhar senha ou resolver CAPTCHA sozinho costuma derrubar a sessão do site e,
pior, produz ações que ninguém autorizou.

---

### 3. Panorama: as duas metades

O projeto tem duas metades que evoluíram em ordem:

```
┌─────────────────────────────────────────────────────────────┐
│  METADE 1 — O COMPUTADOR  (infraestrutura)                  │
│                                                             │
│  volume durável 20 GB          droplet s-2vcpu-4gb          │
│  agent-computer-workspace      (descartável por construção) │
│         │                              │                    │
│         └── /workspace ──────────────  ├── /scratch         │
│              ├── browser/screen-N      ├── pacotes          │
│              ├── projects/             └── sistema          │
│              └── agent/                                     │
│                   ├── tasks/                                │
│                   ├── conversations/                        │
│                   ├── locks/                                │
│                   └── status/                               │
│                                                             │
│  Xvfb :N + Openbox + x11vnc + noVNC + Chrome  (por tela)     │
└─────────────────────────────────────────────────────────────┘
                              ▲
                              │ opera
┌─────────────────────────────────────────────────────────────┐
│  METADE 2 — O AGENTE  (agentd, Go hexagonal)                │
│                                                             │
│  cmd/agentd ──▶ service.Agent ──▶ ports ──▶ adapters        │
│                      │                        ├── xai       │
│                      │                        ├── tools     │
│                   domain                      ├── screen    │
│                (regras puras)                 ├── store     │
│                                               └── lock      │
└─────────────────────────────────────────────────────────────┘
```

**Metade 1** responde "onde o trabalho acontece e o que sobrevive".
**Metade 2** responde "quem decide o que fazer e como ele pede ajuda".

---

### 4. A metade 1: o computador

#### 4.1 Por que o droplet é descartável

Esta é a decisão estrutural do projeto, e ela veio de um erro.

A primeira versão colocava `/workspace` no disco do droplet. Tudo funcionava — e
o verbo **Update** era impossível de implementar. Não há como "reconstruir o
computador com uma imagem nova preservando o estado durável" se o estado e a
imagem são o mesmo disco.

A correção separa os dois:

```
  ┌────────────────────────┐        ┌──────────────────────────┐
  │  VOLUME (durável)      │        │  DROPLET (descartável)   │
  │  20 GB, US$ 2,00/mês   │◀──────▶│  4 GB, US$ 24,00/mês     │
  │                        │ monta  │                          │
  │  /workspace            │        │  sistema, pacotes        │
  │   ├── browser/         │        │  /scratch                │
  │   ├── projects/        │        │                          │
  │   └── agent/           │        │  pode ser destruído a    │
  │                        │        │  qualquer momento        │
  └────────────────────────┘        └──────────────────────────┘
```

Consequência prática que vale dinheiro: `task destroy` derruba o droplet e o
trabalho **continua no volume** por US$ 2/mês, em vez de US$ 26. `task up` traz
tudo de volta.

⚠️ **A armadilha aqui é silenciosa.** Se o volume for anexado *depois* da criação,
o cloud-init roda antes de o device existir, cria `/workspace` no disco local, e
**tudo funciona** — até o primeiro update, quando o trabalho some sem aviso. Por
isso o volume vai no corpo da requisição de criação, há espera explícita pelo
device, e `nofail` no `fstab` (sem ele, um boot sem o volume cai em emergency
shell, onde nem SSH existe para diagnosticar).

A validação cobre exatamente isso:

```bash
if agent_ssh 'mountpoint -q /workspace'; then
  ok "/workspace é ponto de montagem (volume separado)"
else
  fail "/workspace está no DISCO DO DROPLET — some num update"
fi
```

#### 4.2 As telas

Cada tela é uma instância de cinco units systemd, criadas por template:

```
tela N ──┬── xvfb@N      Xvfb :N -screen 0 1920x1080x24
         ├── openbox@N   gerenciador de janelas, DISPLAY=:N
         ├── x11vnc@N    serve a tela em 127.0.0.1:(5900+N)
         ├── novnc@N     a mesma tela via web em 127.0.0.1:(6080+N)
         └── chrome@N    perfil em /workspace/browser/screen-N
                         CDP em 127.0.0.1:922N
```

Somar o número da tela à porta base evita tabela de portas em qualquer lugar:

| Tela | VNC | web | CDP | perfil |
|---|---|---|---|---|
| 1 | 5901 | 6081 | 9221 | `/workspace/browser/screen-1` |
| 2 | 5902 | 6082 | 9222 | `/workspace/browser/screen-2` |
| N | 5900+N | 6080+N | 922N | `/workspace/browser/screen-N` |

Criar a tela 2: `screen-add 2`. Custo medido: **~500 MB de RAM por tela**
(980 MB com uma, 1,5 GB com duas), então 4 GB comportam cerca de cinco.

#### 4.3 Acesso: nada é publicado

```
   Seu Mac                    túnel SSH                  Droplet
   ┌──────────┐                                    ┌──────────────┐
   │ navegador│──▶ 127.0.0.1:6081 ══════════════▶ │ 127.0.0.1:6081│
   │          │                                    │  noVNC        │
   └──────────┘         porta 22 é a ÚNICA         │ 127.0.0.1:5901│
                        aberta no ufw              │  x11vnc       │
                                                   │ 127.0.0.1:9221│
                                                   │  Chrome CDP   │
                                                   └──────────────┘
```

VNC, noVNC e CDP escutam **apenas em loopback** dentro do droplet. `ufw` deixa
passar só a 22.

**A troca assumida:** quem tem a chave SSH tem o desktop, sem segunda senha. Para
um lab de duas pessoas isso é melhor que uma senha VNC exposta na internet. Para
ampliar sem dar SSH, o caminho é autenticar o Tailscale e compartilhar o nó.

#### 4.4 Os três verbos

| Verbo | Comando | Preserva | Descarta | Como funciona |
|---|---|---|---|---|
| **Update** | `task update` | `/workspace`, perfis, sessões | `/scratch`, pacotes manuais, sistema | destaca o volume → destrói o droplet → cria outro com imagem nova → remonta |
| **Reset** | `task reset` | só o que está no snapshot | trabalho posterior ao snapshot | cria volume novo a partir do snapshot → troca pelo atual |
| **Recover** | `task update` | idem Update | idem Update | com o estado no volume, recuperar um droplet inalcançável **é** reconstruí-lo |

---

### 5. A metade 2: o agente

#### 5.1 Por que hexagonal

O agente precisa ser testável sem rede, sem disco e sem servidor X. Se o laço de
decisão dependesse diretamente da API da xAI, cada teste custaria token e
dependeria da internet — e ninguém rodaria os testes.

```
        ┌──────────────────────────────────────────────┐
        │                  domain                      │
        │  Task, Conversation, SecretRequest           │
        │  ZERO imports externos                       │
        │  cobertura: 100%                             │
        └──────────────────────────────────────────────┘
                          ▲
                          │ usa
        ┌──────────────────────────────────────────────┐
        │                 service                      │
        │  Agent — o laço: modelo → ferramenta →       │
        │  estado → tela                               │
        └──────────────────────────────────────────────┘
                          ▲
                          │ fala só com interfaces
        ┌──────────────────────────────────────────────┐
        │                  ports                       │
        │  LanguageModel  Tool  ScreenDriver           │
        │  TaskStore  ScreenLock  SecretPrompter       │
        └──────────────────────────────────────────────┘
                          ▲
                          │ implementam
        ┌──────────────────────────────────────────────┐
        │            adapters/driven                   │
        │  xai   tools   screen   store   lock         │
        └──────────────────────────────────────────────┘
                          ▲
                          │ monta tudo
        ┌──────────────────────────────────────────────┐
        │              cmd/agentd                      │
        │  único lugar que conhece implementações      │
        └──────────────────────────────────────────────┘
```

A direção das setas é a regra: **adapters → ports → domain**, nunca o contrário.
`domain` não importa nem `ports` — por isso o laço mora em `service`.

#### 5.2 O laço, passo a passo

```
  Run(task)
     │
     ├─▶ 1. toma a trava da tela  ──── ocupada? erro imediato
     │
     ├─▶ 2. task.Start()  →  grava  →  status na tela
     │
     ├─▶ 3. carrega ou cria a conversa
     │
     └─▶ 4. LAÇO (até 60 iterações)
             │
             ├── Trim(80 mensagens) e grava a conversa
             │
             ├── modelo.Complete(histórico, ferramentas)
             │      │
             │      └── erro? → task.Fail() → propaga
             │
             ├── conversa.AddAssistant(resposta, chamadas)
             │
             ├── SEM chamadas? → task.Finish() → FIM ✅
             │
             └── PARA CADA chamada:
                    │
                    ├── ferramenta desconhecida? → diz ao modelo, continua
                    ├── executa
                    ├── erro de execução? → vira texto no histórico, continua
                    │
                    └── veio BlockRequest?
                           ├── task.Block(motivo, detalhe)
                           ├── tela mostra o pedido de ajuda
                           └── PARA O LAÇO ⏸  ← a cláusula central
```

Três decisões embutidas aí, que merecem explicação:

**(a) Falha de ferramenta não derruba a tarefa.** O erro vira conteúdo no
histórico e o modelo se recupera na iteração seguinte. Abortar a cada saída
diferente de zero tornaria o agente inútil — `grep` sem resultado já devolve 1.

**(b) Teto de 60 iterações.** Sem ele, um agente que erra a mesma chamada em ciclo
queima token até alguém perceber. 60 é generoso para tarefa de navegador, onde
cada clique é uma iteração.

**(c) Bloqueio para o laço imediatamente.** Continuar chamando o modelo enquanto a
pessoa não agiu é exatamente "tentar contornar a verificação".

#### 5.3 As ferramentas

| Ferramenta | O que faz | Detalhe que importa |
|---|---|---|
| `shell` | executa comando | usa `bash -c`, **não** `-lc` — ver §9.4 |
| `request_takeover` | pede que uma pessoa assuma | é o que transforma "preciso de ajuda" em estado executável |
| `browser_*` | navega, lê, clica, preenche, fotografa | fala CDP direto com o Chrome da tela — ver §4 |
| `delegate_to_code` | entrega trabalho de código a outro agente | ver §5.6 |
| `<conector>.<operação>` | chama a API de um serviço | só entra quando anexado com `@` — ver §5.4 |

#### 5.4 Conectores

Um conector é declarado em **manifesto**, não em código: instalar um serviço novo
não recompila nada. Os dois formatos são aceitos, e servem a públicos diferentes:

| Formato | Para quem |
|---|---|
| `.json` | o que uma ferramenta gera |
| `.yaml` / `.yml` | o que uma pessoa escreve — aceita comentário |

Exemplos versionados: `examples/connectors/github.json` e
`examples/connectors/gitlab.yaml`. O segundo mostra o que o YAML acrescenta:
comentário explicando que o token precisa do escopo `api`, e que `per_page`
acima de 100 é ignorado em silêncio pela API — coisas que não cabem em JSON e
que a pessoa seguinte descobriria na marra.

```
                 texto da tarefa
                        │
          "@gitlab liste as issues do projeto 12345"
                        │
              ParseTaskRequest (domínio)
                        │
        ┌───────────────┴───────────────┐
   Connectors: [gitlab]          Prompt: "liste as issues
        │                                 do projeto 12345"
        ▼                                        │
   Registry.ToolsFor                             ▼
        │                                  vai ao modelo
   gitlab.list_issues                      SEM o marcador
   gitlab.create_issue
```

**Só os conectores anexados entram.** A descrição de cada ferramenta vai no
prompt a cada iteração, então oferecer o catálogo inteiro custaria token em toda
chamada e daria ao modelo acesso a serviços que a tarefa não pediu.

**A credencial nunca está no manifesto** — só a referência a ela. Manifesto é
copiado, versionado e compartilhado sem ninguém reparar; e como conectores são
de conta, o valor ficaria ao alcance de todo agente. O segredo mora em
`connectors/secrets/`, com permissão `0600`, e é lido do disco a cada chamada em
vez de ficar em memória de um processo de vida longa.

---

#### 5.5 Habilidades salvas

Uma habilidade é um procedimento reutilizável guardado no volume durável. Em vez
de colar dez linhas explicando como publicar um release a cada tarefa,
escreve-se `/release`.

```
/workspace/agent/skills/
  ├── release.md
  ├── revisao.md
  └── deploy.md
```

O bloco entra no texto **delimitado e nomeado**:

```
liste as issues abertas do projeto 12345

--- habilidade salva: release ---
1. rode `task test:cov` e confirme o gate verde
2. crie a tag assinada
3. publique
--- fim de release ---
```

Três decisões pequenas com motivo:

**A delimitação não é enfeite.** Sem ela, um procedimento longo se mistura ao
pedido e o modelo passa a tratar o procedimento como o objetivo — responde
descrevendo como publicaria um release em vez de listar as issues.

**O bloco entra depois do pedido**, para o objetivo vir primeiro. Instrução longa
antes da tarefa desloca a atenção do modelo pelo mesmo motivo.

**O limite de 8 KB protege custo, não disco.** O conteúdo entra no prompt a cada
iteração da tarefa, não uma vez.

##### A habilidade `/web-search`, e por que ela existe

Buscar da nuvem é hostil, e a habilidade nasceu dessa medição — não de teoria.
Sondado do próprio droplet em 30/08/2026 (`scripts/18-probe-web-sources.sh`):

| Fonte | Devolve | Leitura |
|---|---|---|
| Google | `200`, 92 KB | **bloqueio disfarçado** — código diz sucesso |
| DuckDuckGo (html e lite) | `202` | é 2xx, e é o desafio anti-bot |
| Startpage | `200`, 22 KB | idem Google |
| Brave | `429` | e a página contém a palavra "result", o que engana um teste de conteúdo |
| AwesomeAPI | `429` | recusa faixa de nuvem |
| ESPN | `403` | |
| Globo Esporte | `200`, **858 KB** | casca vazia; o conteúdo vem por JavaScript |
| **Bing** | `200`, 124 KB | ✅ e tem `format=rss`, que devolve XML limpo |
| **Mojeek**, **Wikipedia**, **CoinGecko**, **Open-Meteo**, **wttr.in**, **Frankfurter** | `200` | ✅ |

**Nem o código nem o conteúdo decidem sozinhos.** O Google devolve `200` com
página de bloqueio; o Brave devolve `429` numa página cujo texto casa com
"result". A sonda exige as duas coisas — e só `200`, não a faixa `2xx`, porque o
`202` do DuckDuckGo é recusa.

A habilidade ordena a busca do mais barato ao mais geral:

| Passo | Quando | Custo |
|---|---|---|
| 1. `date` | sempre que a pergunta tiver palavra de tempo | ~0 |
| 2. atalho direto | dólar, cripto, temperatura | 1 `curl`, <1 s |
| 3. `delegate_to_code` | qualquer outra pergunta | 1 inferência |
| 4. Bing com `format=rss` | se a delegação falhar | 1 `curl` |
| 5. `browser_navigate` | conteúdo que só existe após JavaScript | — |

O passo 1 não é detalhe: `date` devolveu `Sunday, 30/08/2026`, e foi só por isso
que a resposta do dólar saiu **certa** — R$ 5,1641 é de sexta, 28/08, porque não
há cotação em fim de semana. Sem descobrir o dia, o agente entregaria um número
de dois dias atrás como se fosse de agora.

**Provado com quatro perguntas** (`scripts/21-web-search-test.sh`):

| Pergunta | Rota tomada | Chamadas |
|---|---|---|
| cotação do dólar | atalho Frankfurter | 3 |
| preço do bitcoin | atalho CoinGecko | 2 |
| temperatura no Rio | atalho Open-Meteo — e cruzou com wttr.in por conta própria, explicando a divergência entre modelos | 1 |
| jogos de hoje | delegação | 2 |

##### A falha que quase passou: permissão devolvida como resposta

Na primeira execução, a pergunta dos jogos gastou **20 chamadas** raspando HTML
com regex em Python, e não respondeu.

A causa não era o Grok. O Claude Code em `-p` **não libera `WebSearch` por
padrão**, e devolveu isto:

```
A ferramenta de busca na web ainda não foi liberada — preciso que você
aprove a permissão de "WebSearch". Pode aprovar?
```

Texto normal, **código de saída zero**. Quem delegou leu como se fosse a
resposta, concluiu que buscar era impossível, e partiu para a raspagem. É o pior
modo de falha que esta ferramenta tem, porque não se parece com falha.

A correção é `--allowedTools` com lista explícita — e **não**
`--permission-mode bypassPermissions`: o que se quer liberar é pesquisa e
edição, não uma procuração ampla num computador que guarda credencial de conta.
Com a flag, a mesma pergunta passou de 20 chamadas para **2**.

##### Autenticação: assinatura, não chave de API

Em 30/08 a conta de API do computador ficou sem saldo no meio dos testes
(`Credit balance is too low`) e derrubou a delegação. O token de assinatura
(`claude setup-token`) usa o plano que já existe.

| | |
|---|---|
| credencial | `/workspace/agent/anthropic.env`, `0600` — aceita `ANTHROPIC_API_KEY` **ou** `CLAUDE_CODE_OAUTH_TOKEN` |
| configuração | `/workspace/agent/claude-config/` — volume **durável**; o padrão `~/.claude` morreria no `update` |
| no cofre | `pass show bassi/anthropic/claude-code-token` |

⚠️ **`claude setup-token` exige terminal de verdade.** De qualquer shell sem tty
ele falha com `tcgetattr/ioctl: Operation not supported on socket`, e nem
`script -q` contorna — o próprio `script` precisa do tty que não existe. Por
isso `scripts/20-setup-subscription-token.sh` lê o token do **stdin**:

```bash
claude setup-token | scripts/20-setup-subscription-token.sh
```

O valor nunca é impresso: entra por `mktemp` sob `umask 077`, vai ao `pass` e ao
droplet, e o arquivo morre em `shred`.

⚠️ **O arquivo de credencial vence o ambiente herdado**, e há teste para isso.
Sem essa precedência, uma `ANTHROPIC_API_KEY` velha no ambiente mascararia o
token e a delegação falharia por saldo — apontando o diagnóstico para a conta
errada.

O nome vem do texto que a pessoa digitou, depois de passar pelo parser de
marcadores, e por isso é validado antes de virar caminho de arquivo — um nome com
subida de diretório gravaria fora da pasta.

Habilidade inexistente vira **aviso**, não erro fatal: a pessoa pode ter digitado
errado, e derrubar a tarefa inteira por um nome trocado é pior do que seguir
dizendo o que faltou.

#### 5.6 Delegação: quando o Grok chama o Claude Code

O computador tem **dois** agentes instalados, e eles não se substituem. Delegar é
o mecanismo que deixa cada um fazer o que faz melhor dentro de **uma** tarefa.

| | Grok (`agentd`) | Claude Code |
|---|---|---|
| navegar, ler página, clicar | ✅ pela CDP, na tela que ele já tem | ❌ não tem tela |
| chamar API por conector | ✅ manifesto, sem código | ⚠️ escreveria o `curl` na hora |
| **pedir take-over e parar** | ✅ é a razão de ele existir | ❌ não tem esse conceito |
| editar arquivo, mexer em git | ⚠️ só por `shell`, um `sed` por vez | ✅ é o ofício dele |
| refatorar, rodar teste, corrigir | ⚠️ caro e desajeitado | ✅ abre subagente, itera sozinho |
| rodar comando | ✅ | ✅ (é onde se sobrepõem, e só) |

**O caso que justifica a ferramenta é o misto** — *"leia o site e ajuste o código
conforme"*. Nenhum dos dois entrega isso sozinho: o Claude Code não enxerga a
página, e o Grok escreveria o código a `sed`.

```
tarefa mista
     │
     ├── Grok: browser_navigate + browser_read  ──▶ lê o dado da página
     │
     └── Grok: delegate_to_code("…com o valor X, crie/ajuste …")
                     │
                     └── Claude Code em /workspace ──▶ escreve, roda teste, corrige
                                   │
                     relatório ◀───┘
```

**Três decisões que a ferramenta carrega:**

1. **A descrição diz quando NÃO delegar.** Sem esse limite o modelo delega tudo —
   inclusive o que faria melhor sozinho — e a tarefa paga duas inferências para
   chegar ao mesmo lugar. O texto é explícito: *"NÃO use para navegar, chamar API
   por conector, ou rodar um comando simples"*.
2. **A tarefa vai completa e autossuficiente.** O Claude Code **não vê a conversa
   do Grok**: recebe uma string e mais nada. A descrição avisa isso ao modelo, e é
   por isso que o valor lido da página precisa ir *dentro* do texto delegado.
3. **A credencial fica em arquivo, não no ambiente do processo.** `agentd` é
   processo de vida longa; a chave do Claude Code mora em
   `/workspace/agent/anthropic.env` com permissão `0600` e só entra no ambiente do
   filho, no momento da chamada. Um teste confere isso pelo efeito, com um
   executável falso que imprime `$ANTHROPIC_API_KEY`.

**Limites deliberados:** 15 minutos de prazo (trabalho de código lê vários
arquivos e itera; cortar antes deixaria a árvore pela metade) e 6 000 caracteres
de relatório de volta (ele entra no prompt de **todas** as iterações seguintes do
Grok). Estouro de prazo devolve texto avisando que a árvore pode estar
inconsistente — é o modo de falha mais perigoso, porque o disco fica num estado
que ninguém escolheu.

Falha do Claude Code volta como **texto**, não derruba a tarefa: quem delegou
pode tentar outra abordagem, ou pedir take-over.

**Onde cada metade mora, e por quê:**

| | Onde | O que acontece no `update` |
|---|---|---|
| binário `claude` | `/usr/bin` — disco do **sistema** | reinstalado pelo `cloud-init`, na versão corrente |
| credencial | `/workspace/agent/anthropic.env`, permissão `0600` — volume **durável** | volta sozinha |

A divisão é deliberada: o binário no disco do sistema faz cada rebuild pegar a
versão atual, em vez de arrastar uma versão velha presa no volume. A credencial
no volume é o que dispensa reconfigurar depois de todo `update`.

⚠️ **`runcmd` não aborta quando um comando falha.** Um `npm install -g` morto por
rede ou registry fora deixaria o computador sem agente de código, com o
cloud-init reportando sucesso e a marca `READY` no lugar — e a falha só
apareceria na primeira delegação, como *"o agente de código não está
configurado"*, mensagem que manda procurar no arquivo de credencial em vez de na
instalação.

Fechado com três coisas: **retry** (3 tentativas), **marcador** em
`/var/lib/agent-computer-code-agent` com a versão ou `FALHOU`, e a **seção 10 do
`task validate`**, que separa "binário ausente" de "credencial ausente" — são
diagnósticos diferentes e a correção de cada um é outra.

---

### 6. Fluxo ponta a ponta

#### 6.1 Tarefa que conclui sozinha

```
você            agentd          Grok           shell        tela
 │                │               │              │            │
 ├─ -prompt ─────▶│               │              │            │
 │                ├─ trava tela ──┼──────────────┼───────────▶│ "trabalhando"
 │                ├─ histórico ──▶│              │            │
 │                │◀─ shell(nproc)┤              │            │
 │                ├───────────────┼─────────────▶│            │
 │                │◀──────────────┼── "2" ───────┤            │
 │                ├─ resultado ──▶│              │            │
 │                │◀─ "pronto" ───┤              │            │
 │                ├─ Finish ──────┼──────────────┼───────────▶│ "concluída"
 │◀─ estado final ┤               │              │            │
```

**Executado de verdade em 29/08/2026**, contra o `grok-4.6`:

```
tarefa teste-shell na tela 1
estado final: tela 1: concluída

  [assistant] -> shell({"command":"nproc && mkdir -p /workspace/projects && nproc > ...})
  [tool] 2 2 -rw-rw-r-- 1 agent agent 2 Aug 30 02:17 /workspace/projects/cpus.txt
```

O agente contou os núcleos, gravou o arquivo, **conferiu com `ls`** e concluiu.

#### 6.2 Tarefa que esbarra numa senha

```
você            agentd          Grok        takeover        tela
 │                │               │             │             │
 ├─ -prompt ─────▶│               │             │             │
 │                ├─ histórico ──▶│             │             │
 │                │◀─ request_takeover(password)─┤             │
 │                ├──────────────────────────────┼────────────▶│
 │                │◀── BlockRequest ─────────────┤             │
 │                ├─ task.Block(password)        │             │
 │                ├──────────────────────────────┼────────────▶│ "PRECISA DE VOCÊ"
 │                ├─ LAÇO PARA ⏸                 │             │
 │◀─ "abra a tela e resolva" ─────┤              │             │
 │                                                             │
 ├─ abre task open, digita a senha na tela ───────────────────▶│
 │                                                             │
 ├─ -resume ─────▶│                                            │
 │                ├─ ClearTakeover ───────────────────────────▶│
 │                ├─ conversa += "resolvido, continue"         │
 │                └─ retoma o laço                             │
```

**Executado de verdade**, com o prompt de entrar num painel que pede senha:

```
estado final: tela 1: PRECISA DE VOCÊ — precisa de senha ou passkey

State: blocked
BlockReason: password
BlockDetail: Faça login no painel https://painel.exemplo.com com o usuário admin
             (a página pede senha). Depois de autenticar, devolva o controle.
```

E a trava se comporta certo nos dois sentidos:

```
a trava da tela foi liberada?       SIM, tela livre para outra tarefa
a tela recusa outra tarefa?         erro: a tela já tem uma tarefa ativa
```

Parece contraditório e não é: o **processo** solta o `flock` (não fica segurando a
tela enquanto espera a pessoa, que pode demorar horas), mas o **estado** da tarefa
continua ocupando a tela. Duas travas, propósitos diferentes.

---

#### 6.3 Tarefa com conector e habilidade

```
agentd -prompt "@gitlab siga /release e grave o log em /workspace/projects/saida.txt"
                 │            │                        │
                 │            │                        └── caminho: NÃO é marcador
                 │            └── habilidade: injetada delimitada, depois do pedido
                 └── conector: 2 ferramentas anexadas ao agente

        ParseTaskRequest (domínio)
                 │
    ┌────────────┼────────────────────────────┐
    ▼            ▼                            ▼
Connectors    Skills                       Prompt
[gitlab]      [release]      "siga e grave o log em /workspace/projects/saida.txt"
    │            │                            │
    ▼            ▼                            │
Registry     skills.Expand ──── concatenado ──┘
.ToolsFor         │
    │             └─▶ "--- habilidade salva: release --- ... --- fim de release ---"
    ▼
gitlab.list_issues
gitlab.create_issue
```

**Verificado contra o Grok de verdade**, com um manifesto YAML instalado:

```
conectores anexados: gitlab (2 ferramentas)
habilidades aplicadas: estilo

resposta do Grok: gitlab.list_issues, gitlab.create_issue
```

E as três garantias, conferidas no histórico gravado: marcadores removidos do
texto, habilidade injetada e delimitada, caminho de arquivo preservado.

---

### 7. As cláusulas, uma a uma

Esta é a seção central. Cada entrada tem: o que a documentação exige, por que isso
existe, como foi implementado, e como sabemos que funciona.

---

#### C1 — Um agente roda uma tarefa por tela de cada vez

**Por que existe.** Duas tarefas disputando o mesmo teclado e o mesmo mouse não
falham com erro: elas produzem cliques intercalados que fazem a coisa errada, em
silêncio.

**Implementação.** Duas camadas, de propósito:

1. **`flock` do sistema operacional** (`internal/adapters/driven/lock`). Precisa
   ser trava de núcleo, e não um registro em arquivo: dois processos que leem
   "livre" e escrevem "ocupado" podem fazê-lo no mesmo instante e ambos seguirem.
   O `flock` também é liberado sozinho se o processo morrer — um arquivo de
   estado deixaria a tela travada para sempre depois de uma queda.

2. **Estado da tarefa** (`Task.Active()`), que inclui `blocked`. Uma tarefa
   esperando a pessoa continua reservando a tela.

```go
// A tentativa é não bloqueante de propósito: se a tela está ocupada, quem
// chamou precisa saber agora, e não ficar pendurado sem explicação.
if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
    owner, _ := os.ReadFile(path)
    return nil, fmt.Errorf("%w (tela %d, ocupada por %q)", domain.ErrScreenBusy, screen, string(owner))
}
```

**Como sabemos.** O teste gera um **segundo processo** (`flock -n`) — testar
dentro do mesmo processo não provaria nada. E no droplet: a segunda invocação foi
recusada com *"a tela já tem uma tarefa ativa"*.

---

#### C2 — As telas não são fronteiras de segurança

**Por que existe.** É um aviso, não um recurso. Quem assume que a tela isola vai
gravar credencial de um cliente numa tela achando que a outra não alcança.

**Implementação.** Não há o que implementar — há o que **documentar onde alguém
vai ler**: no `screen-add`, no README e aqui.

```bash
## Cria mais uma tela na MESMA maquina. As telas sao superficies de trabalho
## separadas, NAO fronteiras de seguranca -- quem alcanca uma alcanca o mesmo
## /workspace e as mesmas credenciais de linha de comando.
```

---

#### C3 — A visualização mostra o status atual

**Por que existe.** Ver a tela mostra *o que está acontecendo*; o status diz *em
que pé está*. Sem ele, uma tela parada é indistinguível de uma tela travada.

**Implementação.** `Task.StatusLine()` mora no **domínio**, porque é regra de
produto, não de apresentação:

```go
case StateBlocked:
    return fmt.Sprintf("tela %d: PRECISA DE VOCÊ — %s", t.Screen, t.BlockReason.Description())
```

O driver escreve em **dois lugares** de propósito: num arquivo (que ferramentas de
fora leem e que sobrevive à queda do X) e no nome da raiz do X. Depender só do X
deixaria o status invisível para quem consulta por SSH.

**Como sabemos.** Um teste garante que **todo** estado produz linha não vazia —
um estado sem texto viraria faixa em branco, igual a overlay quebrado. Outro
garante que a falha do X **não** derruba a gravação.

---

#### C4 — O agente pode pedir que você assuma

**Por que existe.** É a cláusula mais importante da documentação, e a mais fácil
de implementar mal.

**A implementação errada** seria o agente escrever "preciso de ajuda" na resposta.
Isso é texto: no turno seguinte ele continua agindo, porque nada o impede.

**A implementação certa** é uma **ferramenta**, cuja chamada muda o estado:

```go
return &ports.ToolResult{
    Output:       fmt.Sprintf("aguardando a pessoa: %s", args.Detail),
    BlockRequest: &ports.BlockRequest{Reason: reason, Detail: args.Detail},
}, nil
```

e no laço:

```go
if result.BlockRequest != nil {
    task.Block(req.Reason, req.Detail, a.clock())
    a.screen.RequestTakeover(ctx, task.Screen, req.Reason, req.Detail)
    return true, nil   // ← para o laço
}
```

A descrição da ferramenta é deliberadamente enfática, porque é ela que ensina o
comportamento ao modelo:

> Use SEMPRE que encontrar senha, passkey, verificação em duas etapas, CAPTCHA,
> confirmação de pagamento ou identidade, ou um site que exija uma pessoa. Nunca
> tente contornar, adivinhar ou burlar essas barreiras.

**Como sabemos.** Teste com duplo: o roteiro tem uma segunda resposta que **não
deve ser consumida** — se for, o laço não parou. E no droplet, com o Grok real:
diante de uma tela de login, ele parou e pediu ajuda.

---

#### C5 — Os cinco gatilhos são fechados

**Por que existe.** Quem escolhe o motivo é o modelo, e modelo inventa valor. Um
motivo desconhecido bloquearia a tarefa sem a tela saber o que pedir à pessoa.

**Implementação.** `BlockReason` com exatamente os cinco da documentação, validado
antes de virar estado:

```go
func ValidBlockReason(r BlockReason) bool {
	switch r {
	case BlockPassword, BlockTwoFactor, BlockCaptcha, BlockPaymentIdentity, BlockHumanRequired:
		return true
	}
	return false
}
```

Motivo inválido **não** bloqueia: volta ao modelo como erro, com a lista, e ele
corrige na iteração seguinte.

---

#### C6 — Senha não entra na conversa

**Por que existe.** Um valor sigiloso que entra no histórico é reenviado ao modelo
a cada iteração seguinte, gravado em disco, e possivelmente registrado em log.

**Implementação — a garantia é estrutural.** O tipo simplesmente **não tem campo**
para o valor:

```go
type SecretRequest struct {
	ID          string
	Label       string      // "senha do painel" — o quê, sem revelar
	Destination string      // para onde vai, para a pessoa conferir
	Fulfilled   bool        // que foi respondido; o valor NÃO fica aqui
}
```

> Um campo `Value string`, ainda que sempre limpo depois, criaria a chance de o
> valor ser serializado num log, num dump de estado ou no próximo turno. **Não
> existir é a única garantia que não depende de alguém lembrar de limpá-lo.**

O que entra no histórico:

```
[segredo fornecido: senha do painel, destino painel.exemplo.com]
```

**Segunda linha de defesa:** `Redact()` apaga ocorrências literais antes de
qualquer envio, porque um segredo pode reaparecer ecoado por um shell ou dentro
do HTML de uma página. Valores com menos de 4 caracteres são ignorados de
propósito — apagar uma cadeia curta destruiria o texto sem proteger nada.

**Como sabemos.** Um teste usa **reflexão** para falhar se alguém acrescentar um
campo capaz de guardar o valor:

```go
proibidos := map[string]bool{"value": true, "secret": true, "password": true, ...}
typ := reflect.TypeOf(SecretRequest{})
for i := 0; i < typ.NumField(); i++ {
    if proibidos[strings.ToLower(typ.Field(i).Name)] {
        t.Fatalf("campo %q guardaria o segredo — ele nunca deve ser retido", ...)
    }
}
```

É a única forma de a promessa sobreviver a uma refatoração distraída.

---

#### C7 — Pausar e avisar, em vez de contornar

**Implementação.** É o mesmo `request_takeover` da C4, mais a regra nº 1 da
instrução de sistema do agente:

```
1. NUNCA tente contornar senha, verificação em duas etapas, CAPTCHA, confirmação
   de pagamento ou verificação de identidade. Ao encontrar qualquer uma delas,
   chame request_takeover e PARE.
```

⚠️ **Detalhe que quase se perde:** o `Trim` do histórico preserva **sempre** a
primeira mensagem, que é a instrução de sistema. Cortar do começo sem essa
ressalva removeria justamente as regras de conduta, e o efeito prático seria o
agente voltar a fazer o proibido depois de algumas dezenas de turnos.

---

#### C8 — Durável × descartável

**Implementação.**

| Durável | Descartável |
|---|---|
| `/workspace` (volume separado) | `/scratch` (limpo por `tmpfiles.d`) |
| `/workspace/browser/screen-N` | pacotes instalados à mão |
| `/workspace/agent/` | o sistema inteiro |

A instrução de sistema ensina isso ao agente, e a descrição da ferramenta `shell`
repete — porque é o agente que decide onde gravar.

**Como sabemos.** O teste `12-update-test.sh` marca os **dois lados** da fronteira,
destrói o droplet de verdade, e confere que cada marca teve o destino certo:

```
--- o que DEVE ter sobrevivido ---
  ✅ /workspace/DURAVEL.txt preservado
  ✅ projeto em /workspace preservado
  ✅ perfil do navegador preservado
--- o que DEVE ter sido descartado ---
  ✅ /scratch descartado, como a doc manda
  ✅ pacote instalado na mao descartado
```

Testar só o lado que sobrevive não provaria a fronteira — provaria só que o disco
funciona.

---

#### C9 — Update, Recover e Reset são diferentes

Já detalhado em §4.4. O ponto conceitual: **Update preserva o estado como ele está
agora; Reset devolve o estado ao snapshot.** Colapsar os dois num só verbo (o erro
da primeira versão) tira do operador a única operação não destrutiva.

---

#### C10 — O computador local é separado

**Implementação.** Por construção: nada em `agentd` toca o Mac. A única ponte é o
túnel SSH que **você** abre.

---

---

#### C11 — Conectores dão uma forma estruturada de usar um serviço

**Por que existe.** A documentação recomenda preferir um conector a clicar pelo
site, dizendo que é mais confiável. O motivo prático: clicar depende de o layout
não ter mudado, de a sessão estar viva e de o elemento estar visível. Uma chamada
de API depende só do contrato.

**Implementação.** Manifesto declarativo, em JSON ou YAML — instalar um serviço
novo não recompila nada:

```yaml
name: gitlab
base_url: https://gitlab.com/api/v4
auth:
  # O token precisa do escopo "api". Um token só de leitura faz list_issues
  # funcionar e create_issue devolver 403 — confuso, porque o conector parece
  # meio quebrado em vez de mal configurado.
  type: header
  header_name: PRIVATE-TOKEN
  secret_ref: gitlab-token
operations:
  - name: list_issues
    method: GET
    path: /projects/{id}/issues
```

Cada operação vira a ferramenta `gitlab.list_issues`. Os dois formatos existem
porque servem a públicos diferentes: JSON é o que uma ferramenta gera, YAML é o
que uma pessoa escreve — e o comentário acima é exatamente o tipo de coisa que
não cabe em JSON e que a próxima pessoa descobriria na marra.

**A credencial nunca está no manifesto**, só a referência a ela. Manifesto é
copiado, versionado e compartilhado sem ninguém reparar; e como conectores são de
conta, o valor ficaria ao alcance de todo agente da máquina. O segredo mora em
`connectors/secrets/` com permissão `0600`, e é lido do disco **a cada chamada**
em vez de ficar em memória de um processo de vida longa, onde apareceria num
dump.

**Como sabemos.** Um teste instala os exemplos versionados de verdade e confere
que cada operação produz esquema válido e descrição não vazia — exemplo que
nenhum teste exercita apodrece, e quem o seguir tropeça num erro que ninguém viu.
E contra o Grok real: perguntado que ferramentas tinha, respondeu
`gitlab.list_issues, gitlab.create_issue`.

---

#### C12 — `@` anexa um conector, `/` referencia uma habilidade

**Por que existe.** São as duas sintaxes que a documentação define para a pessoa
dizer o que a tarefa pode usar.

**Implementação.** O parsing mora no **domínio**, e não no adaptador de linha de
comando, para a regra ficar num lugar só — o dia em que entrar uma segunda porta
de entrada, ela herda o comportamento sem duplicação.

**Só o que foi anexado entra.** A descrição de cada ferramenta vai no prompt a
cada iteração, então oferecer o catálogo inteiro custaria token em toda chamada e
daria ao modelo acesso a serviços que a tarefa não pediu.

**A armadilha, e ela mordeu.** A primeira versão da expressão tratava caminho de
arquivo como habilidade:

```
"grave em /workspace/projects/saida.txt"
   → habilidade "workspace" anexada
   → o caminho REMOVIDO do texto
   → a tarefa quebra em silêncio
```

O Go usa RE2, que não tem lookahead negativo, então a proteção não cabia na
expressão. O que segue o nome é capturado e julgado à parte, distinguindo caminho
(`/workspace/…`, `/saida.txt`) de pontuação de fim de frase (`/release.`).

**Como sabemos.** O teste que cobre isso **falhou na primeira versão** — foi ele
que revelou o defeito, não a leitura do código.

---

### 8. Decisões e por quê

| Decisão | Alternativa | Por que assim |
|---|---|---|
| DigitalOcean | Fly (US$ 5/mês contra US$ 26) | Fly não tem snapshot nativo, que é o `Reset` da documentação |
| droplet 4 GB | 2 GB (US$ 12) | Chrome estoura em 2 GB; OOM no meio do teste polui o resultado |
| nyc3 | outras regiões | 114 ms medidos daqui; acima de ~180 ms o VNC arrasta |
| volume separado | tudo no disco | sem ele, `Update` é impossível |
| `Xvfb` + `x11vnc` + `noVNC` | KasmVNC | KasmVNC daria resolução dinâmica, mas exige `.deb` externo — mais peça para quebrar |
| Chrome | Chromium | o agente navega em site real; compatibilidade importa mais |
| `grok-4.6` | `grok-4.5` | mais recente, com chamada de ferramenta medida |
| manifesto em JSON **e** YAML | só JSON | YAML aceita comentário, e manifesto é escrito à mão: explicar o escopo do token vale mais que economizar uma linha |
| `sigs.k8s.io/yaml` | `gopkg.in/yaml.v3` | converte YAML em JSON e reusa as tags `json` existentes; a alternativa exigiria duplicar cada tag, e tag duplicada diverge com o tempo |
| trava de arquivo | registro em banco | `flock` é liberado sozinho se o processo morrer |
| estado em arquivo JSON | SQLite | precisa sobreviver ao rebuild sem serviço nenhum subir junto |
| perfil por tela | perfil compartilhado | o Chrome trava o `user-data-dir` — ver §10 |

---

### 9. Armadilhas medidas

#### 9.1 O DigitalOcean corrompe user-data não-ASCII

**Custo: três droplets descartados.**

Um `acessível` sai do disco como `C3 AD` e chega no droplet como `C3 83 C2 AD` —
dupla codificação UTF-8 no caminho API → ConfigDrive. O `C2 80` que o travessão
duplo-codificado gera é caractere de controle C1, e o cloud-init recusa o
**arquivo inteiro**.

Três coisas escondem isso:

1. **A recusa é silenciosa.** O droplet reporta `status: done`, sobe, aceita SSH —
   e não instalou nada.
2. **O motivo sai no stderr.** `cloud-init status --long 2>/dev/null` esconde
   justamente os `recoverable_errors`.
3. **Não é culpa do cliente.** Reproduzido em `doctl` 1.145.0, 1.167.0 e na API
   REST direta com payload provado byte-idêntico na origem.

**Correção:** `user-data.yaml` é ASCII puro, com aviso de 18 linhas no topo, e o
gate reprova qualquer byte não-ASCII **antes** de criar o droplet.

#### 9.2 `cmd | tee` engole o código de saída

O primeiro `task up` saiu **rc=0 sem ter criado droplet nenhum**. `set: [pipefail]`
resolve; canário confirma rc=201 com e rc=0 sem.

Vale para `| head` também — aconteceu de novo com `go build ... | head -5 && echo OK`.

#### 9.3 Espera que não distingue estados

A primeira versão do `02-wait-ready.sh` dizia "aguardando SSH responder" quando o
SSH **já autenticava** e só o arquivo-marca faltava — porque `cat` de arquivo
inexistente devolve rc=1. Custou 12 minutos de diagnóstico na direção errada.

Agora separa três estados e **aborta na hora** se o YAML foi recusado.

#### 9.4 `bash -lc` contamina toda saída de comando

Achado por um teste. O shell de **login** carrega o perfil do usuário, e qualquer
mensagem que ele imprima — um `echo` de boas-vindas, um erro de arquivo faltando —
entra na saída de **todo** comando, vai para o histórico do modelo, gasta token e
confunde o agente.

```
esperava marcador de saída vazia, veio
"/Users/andrebassi/.bash_profile: line 19: ... No such file or directory"
```

Corrigido para `bash -c`.

#### 9.5 Cortar saída longa pelo fim perde o erro

Numa saída longa, a mensagem que interessa costuma estar na **última** linha.
`truncateOutput` corta pelo meio, preservando começo e fim.

---

### 10. Divergência conhecida: cookies não são compartilhados entre telas

A documentação diz que logar num site por um agente deixa a sessão disponível
para os outros. **Aqui não acontece.**

**Motivo técnico:** o Chrome trava o `user-data-dir` e recusa um segundo processo
no mesmo diretório. Duas telas com Chrome exigem dois perfis.

**Contornos avaliados, nenhum implementado:**

| Contorno | Por que não |
|---|---|
| um Chrome com janelas em displays diferentes | o Chrome não faz isso |
| sincronizar o banco de cookies entre perfis | SQLite com dois escritores corrompe |
| proxy com jar compartilhado | não cobre `localStorage` nem sessão de aplicação |
| perfil-semente copiado ao criar a tela | resolve parcialmente; login posterior não propaga |

Está registrado como pendência, não como resolvido.

---

### 11. Operação

```bash
## infraestrutura
task check        # binários, token, chave, latência — ANTES de gastar
task up           # volume + droplet + espera + valida
task open         # túnel SSH e a tela no navegador
task screens      # telas ativas, estado durável, recursos
task validate     # as 10 seções
task snapshot     # snapshot do volume durável
task update       # rebuild preservando o durável
task reset        # volta ao snapshot
task destroy      # derruba o droplet (o volume fica, US$ 2/mês)

## o binário do agente
task deploy           # gate de cobertura + compila + instala em /workspace
task delegation-test  # prova a delegação com a tarefa mista (web + código)
task web-search-test  # prova a busca com 4 perguntas reais
task answers          # mostra a RESPOSTA das últimas tarefas, não só o estado

## dentro da máquina
screen-add 2      # cria a tela 2
screen-remove 2   # derruba (o perfil fica)
agent-status      # telas, estado durável, portas, recursos

## o agente
agentd -screen 1 -prompt "a tarefa"
agentd -resume -task <id> -note "resolvi o login"
agentd -abandon -task <id>

## a porta HTTP (outros sistemas chamam por aqui)
task serve-setup    # instala token da API e chave do modelo, do pass
task serve-enable   # sobe o serviço e o timer, e os deixa no boot
task serve-status   # estado do serviço e do timer
task serve-logs     # journal da porta

agentd -serve                        # 127.0.0.1:8787, token em <state>/api-token
agentd -serve -listen 100.x.y.z:8787 # na malha; NUNCA 0.0.0.0

## proatividade (o agente fala primeiro)
agentd -notify-drain                 # lista os avisos pendentes
agentd -notify-drain -webhook <url>  # entrega e limpa a fila

## conectores e habilidades
agentd -prompt "@gitlab liste as issues do projeto 12345"
agentd -prompt "@github siga /release e publique"
```

#### Conectores

O catálogo vive em `/workspace/agent/connectors/`:

```
connectors/
  ├── installed/     manifestos ativos (.json, .yaml ou .yml)
  ├── secrets/       credenciais, 0600, uma por arquivo
  └── available/     catálogo local, para instalar sem baixar
```

Instalar é copiar um manifesto para `installed/` e gravar a credencial em
`secrets/`. Exemplos versionados em `examples/connectors/`:

| Arquivo | Mostra |
|---|---|
| `github.json` | o formato que uma ferramenta geraria |
| `gitlab.yaml` | o que o YAML acrescenta: comentário explicando escopo de token e limite de paginação |

⚠️ **Conectores são de conta.** Instalar um o torna disponível a **todas** as
telas, e a credencial fica ao alcance de qualquer agente da máquina — é o que a
documentação define, e a consequência de as telas não serem fronteira de
segurança. Não instale um conector cujo acesso outro agente não deva ter.

#### Habilidades

```
/workspace/agent/skills/<nome>.md
```

Um arquivo Markdown por habilidade, referenciado com `/<nome>`. Limite de 8 KB —
o conteúdo entra no prompt a cada iteração da tarefa, não uma vez.

#### Testes

```bash
cd agent && ./scripts/coverage-gate.sh   # mede E reprova
```

| Suíte | O que prova |
|---|---|
| `task validate` | 10 seções da infraestrutura, `erros: 0` |
| `09-persistence-test.sh` | reboot real: serviços sobem, sessão sobrevive |
| `12-update-test.sh` | rebuild real: a fronteira durável×descartável vale |
| `coverage-gate.sh` | 91,7% total, domínio 100% |

⚠️ **Sobre cobertura de branch:** o Go não a mede nativamente, só statements. Em
vez de fingir um número que a ferramenta não produz, a compensação é explícita:
tabelas de teste cobrindo cada ramo de decisão (transições inválidas, os cinco
motivos, entradas malformadas), e domínio em 100%.

---

### 12. Números medidos, 29/08/2026

| | |
|---|---|
| Latência até nyc3 | 114 ms |
| Boot + cloud-init | ~4 min |
| Update completo | ~6 min |
| Reboot até serviços ativos | ~70 s |
| RAM com 1 tela | 980 MB de 3915 (25%) |
| RAM com 2 telas | 1,5 GB (~500 MB por tela) |
| Perfil do navegador, uso leve | 286 MB |
| Dependências do módulo Go | 1 direta (`sigs.k8s.io/yaml`), 1 indireta |
| Chrome | 152.0.7977.64 |
| Custo com droplet ligado | US$ 26,00/mês |
| Custo só do estado | US$ 2,00/mês |
| Cobertura de testes | 91,1% (domínio 100%; `delegate.go` 100%) |
| Tokens de uma tarefa simples | 723 entrada / 10 saída |
| Tarefa mista com delegação | ~3 min, 3 chamadas de ferramenta, 2 modelos |
| Claude Code no droplet | 2.1.251 |

---

# Auditoria de fidelidade à documentação


Fonte: <https://docs.x.ai/grok-bot/computer-and-apps> (última atualização na
origem: 11/08/2026). Auditoria: 29/08/2026.

Cada linha é uma afirmação da doc, não uma ideia nossa. `✅` foi implementado e
**testado**; `⚠️` existe parcialmente; `❌` não existe.

### 1. Computador persistente

| Cláusula | Estado | Onde |
|---|---|---|
| "works from a persistent cloud computer" | ✅ | volume durável + droplet |
| "can use a browser" | ✅ | Chrome 152 por tela |
| "command line" | ✅ | SSH como `agent`, `sudo` sem senha |
| "files" | ✅ | `/workspace` no volume |
| "connected tools" | ⚠️ | ferramentas próprias (shell, take-over); sem connectors |
| "without depending on your laptop remaining open" | ✅ | roda no droplet |

### 2. Um computador, compartilhado por todos os Bots

| Cláusula | Estado | Nota |
|---|---|---|
| "Browser cookies and signed-in sessions are shared" | ❌ | perfil por tela |
| "Files are visible to every Bot" | ✅ | `/workspace` comum |
| "Command-line credentials are shared" | ✅ | mesmo usuário |
| "One Bot can continue from work another Bot saved" | ✅ | via `/workspace` |
| "Do not place a credential ... if another Bot should not use it" | ✅ | avisado no README e no `screen-add` |
| "Each Bot gets its own screen" | ✅ | `screen-add N` |
| "one Bot can run only one computer-use task on its screen at a time" | ✅ | flock + estado; testado no droplet |
| "screens are separate work surfaces, not separate security boundaries" | ✅ | documentado |

### 3. Assistir ao trabalho

| Cláusula | Estado | Nota |
|---|---|---|
| "view the shared desktop" | ✅ | noVNC pelo túnel |
| "shows clicks, typing, navigation" | ✅ | é a tela real |
| "and current status" | ✅ | linha de status por tela, em arquivo e no X |
| "leave the preview while work continues" | ✅ | desacoplado |
| "closing the app or laptop does not stop cloud work" | ✅ | |

### 4. Assumir o controle num passo sensível

| Cláusula | Estado | Nota |
|---|---|---|
| "The Bot may ask you to take over" | ✅ | ferramenta `request_takeover`; **testado com o Grok de verdade** |
| assumir controle de senha/2FA/CAPTCHA/pagamento | ✅ | tarefa entra em `blocked` e o laço para |
| "Avoid pasting passwords or one-time codes into chat" | ✅ | por construção, não há chat |
| "secure secret request ... masked, not added to the conversation" | ❌ | **não existe** |

### 5. Logar uma vez

| Cláusula | Estado | Nota |
|---|---|---|
| "Browser sessions persist" | ✅ | testado com reboot e rebuild |
| "signing in for one Bot makes the session available to your other Bots" | ❌ | ver §2 |
| "Ask the Bot to pause and notify you rather than bypass the check" | ✅ | é exatamente o que `request_takeover` faz |

### 6. Conectar um app

| Cláusula | Estado | Nota |
|---|---|---|
| "Connectors give a Bot a structured way to work with supported services" | ✅ | manifesto JSON ou YAML vira ferramenta HTTP |
| "type `@` to attach the connector to the task" | ✅ | `ParseTaskRequest`; só o anexado entra |
| "type `/` to reference a saved skill" | ✅ | habilidades em `/workspace/agent/skills` |
| "Installed connectors are account-wide" | ✅ | catálogo no volume, comum a todas as telas |
| "Complete authentication in your browser if requested" | ⚠️ | credencial por arquivo (`-connector-secret`); sem fluxo de navegador |
| "Connectors are shown as **Plugins**" (a tela de catálogo) | ❌ | é interface, não infraestrutura |
| "Prefer a connector when one is available" | ✅ | dito na descrição do conector e nos exemplos |

### 7. Trabalhar com arquivos

| Cláusula | Estado | Nota |
|---|---|---|
| "shared workspace at /workspace" | ✅ | volume |
| "use clear project folders" | ✅ | `/workspace/projects` |
| "files, browser state and sign-ins survive updates and recovery" | ✅ | provado em `12-update-test.sh` |
| "treat temporary directories, manually installed packages ... as replaceable" | ✅ | `/scratch`, provado |
| "copy important results into the shared workspace" | ✅ | |
| "or attach them to the conversation" | ❌ | não há conversa |

### 8. Update, recover, reset

| Cláusula | Estado | Nota |
|---|---|---|
| "Update ... rebuilds with the latest image while preserving durable state" | ✅ | `task update`, testado |
| "Reset ... returns to the most recent durable snapshot" | ✅ | `task reset` |
| "Recover ... replaces an unreachable computer" | ⚠️ | `task update` faz; falta **detectar** inalcançável |
| "When the computer is unreachable, use Recover from the error state" | ❌ | **sem estado de erro** |
| "Wait for active work to finish before recovery when possible" | ❌ | **sem guarda de trabalho ativo** |

### 9. O computador local é separado

| Cláusula | Estado | Nota |
|---|---|---|
| "cloud computer is separate from the Mac in front of you" | ✅ | por construção |
| "only runs commands on your local computer when enabled and approved" | ✅ | nada toca o Mac |

### Placar

| | 29/08 manhã | 29/08 depois do agente | 30/08 |
|---|---|---|---|
| ✅ implementado e testado | 24 | 35 | **36** |
| ⚠️ parcial | 2 | 3 | **3** |
| ❌ ausente | 13 | 4 | **3** |

#### O que ainda falta, e por quê

| Ausente | Motivo |
|---|---|
| tela de catálogo (`Settings → Plugins`) | é interface gráfica, não infraestrutura; o catálogo em si existe |
| secret request como fluxo de tela | o tipo e a garantia existem no domínio; falta a tela que coleta |
| cookies compartilhados entre telas | divergência com motivo técnico — o Chrome trava o `user-data-dir` |

> **Corrigido em 30/08.** *"Detecção de computador inalcançável"* constava aqui
> como ausente enquanto a lista de pendências já a dava por fechada — `task
> health` separa os quatro diagnósticos e indica a recuperação menos destrutiva
> primeiro. As duas listas divergiam; esta estava desatualizada.

#### Provado contra o Grok de verdade, no droplet

| Teste | Resultado |
|---|---|
| tarefa normal | contou os núcleos, gravou `/workspace/projects/cpus.txt`, conferiu com `ls`, concluiu |
| barreira sensível | **parou**, pediu take-over com motivo `password`, tarefa em `blocked` |
| status na tela | `tela 1: PRECISA DE VOCÊ — precisa de senha ou passkey` |
| trava por tela | segunda tarefa recusada: *"a tela já tem uma tarefa ativa"* |
| trava liberada | `flock` livre mesmo com a tarefa bloqueada — o processo não segura a tela esperando a pessoa |
| conector YAML | instalado um manifesto do GitLab; o Grok respondeu `gitlab.list_issues, gitlab.create_issue` — ele enxerga as ferramentas |
| `@` e `/` juntos | `"@gitlab /estilo ... /workspace/projects/saida.txt"` → conector anexado, habilidade injetada, **caminho preservado** |

### O que falta, em ordem de dependência

1. **Nenhum agente roda.** A doc inteira fala de "the Bot". Temos o computador,
   não o bot. Tudo abaixo depende disto.
2. **Sessão compartilhada entre telas** — cláusula citada duas vezes na doc.
3. **Pedido de take-over** — o agente parar, sinalizar e esperar o humano.
4. **Status visível** — o que o agente está fazendo agora, na própria tela.
5. **Trava de uma tarefa por tela.**
6. **Secret request mascarado.**
7. **Guarda de trabalho ativo** antes de update/reset.
8. **Detecção de inalcançável** para acionar recover.
9. **Connectors e skills salvas.**

---

# Avaliação do KasmVNC


Medido em 30/08/2026, no droplet real, com KasmVNC **1.5.0** (`noble`, amd64) ao
lado da pilha atual — não em teoria.

### Veredicto

**Vale trocar**, e por uma margem maior do que eu esperava. Mas não hoje: a
troca é uma mudança de infraestrutura que merece o seu próprio ciclo de teste, e
o que existe funciona.

### Os números

| | trio atual | KasmVNC | |
|---|---|---|---|
| **memória por tela** | **424 MB** | **74 MB** | **−82%** |
| processos por tela | 3 (Xvfb, x11vnc, websockify) | 1 (Xvnc) | |
| resolução | teto fixo de 1920×1080 | **dinâmica** | |
| redimensionar em uso | ❌ `Size not found in available modes` | ✅ 1024×768 → 1920×1080 | |

O ganho de memória é o achado. Com 4 GB de RAM, o trio limita a máquina a cerca
de **cinco telas**; o KasmVNC levaria isso para bem além do que a CPU aguentaria
— o gargalo deixaria de ser memória.

E a resolução dinâmica não é conforto: o Xvfb **recusa** mudar de modo
(`Size 1280x720 not found in available modes`), então a tela nasce e morre em
1920×1080. Quem abrir o noVNC num monitor menor vê a tela cortada ou reduzida.

### O custo real: o caminho até subir

Foram **quatro obstáculos**, nenhum documentado no lugar óbvio:

1. **Escolha interativa de ambiente.** O `kasmvncserver` chama um
   `select-de.sh` que espera resposta num terminal. Num provisionamento
   automático ele falha com `Failed to execute`. Resolve-se com `-select-de manual`
   mais um `~/.vnc/xstartup` escrito à mão.
2. **Exige certificado SSL mesmo com `require_ssl: false`** no
   `~/.vnc/kasmvnc.yaml` — a chave de configuração não vale quando as opções vêm
   por linha de comando.
3. **Permissão do diretório, não do arquivo.** `/etc/ssl/private` é `750 root:ssl-cert`,
   e o erro (`certificate file doesn't exist or isn't a file`) descreve o sintoma
   errado: o arquivo existe, o que falta é poder listar o diretório. Resolve com
   `usermod -aG ssl-cert`.
4. **O grupo novo não vale na sessão em curso.** Precisa de `sg ssl-cert -c`, ou
   de uma sessão nova, senão a permissão recém-dada não tem efeito e parece que
   o passo 3 não funcionou.

Some-se um detalhe de diagnóstico: **o processo chama `Xvnc`, não `Xkasmvnc`**.
Procurar pelo nome do produto faz concluir que ele não subiu quando subiu — foi
exatamente o que aconteceu aqui.

### O que a troca exigiria

- reescrever as cinco units de template (`xvfb@`, `x11vnc@`, `novnc@` viram uma só)
- resolver os quatro pontos acima no `cloud-init`, em ASCII puro
- refazer o mapa de portas: hoje `5900+N` e `6080+N` viram uma porta só
- reescrever as seções 2, 3 e 5 do `task validate`
- rodar de novo o teste integrado inteiro

### Por que não agora

O que existe funciona, está testado ponta a ponta e não está limitando nada: as
telas usadas até aqui são uma ou duas, bem longe do teto de cinco. Trocar a
camada de vídeo por ganho de memória que ninguém está consumindo é otimizar o
item errado — o mesmo raciocínio que já vale para a inferência custar mais que o
servidor.

**O gatilho para fazer a troca:** precisar de mais de três telas simultâneas, ou
alguém reclamar de tela cortada por resolução fixa. Aí o ganho deixa de ser
teórico.

---

# Avaliação do CloakBrowser

Avaliado em **30/08/2026**, a partir do repositório
<https://github.com/CloakHQ/CloakBrowser>. **Não foi instalado nem executado** —
a avaliação parou antes disso, e a seção seguinte explica por quê.

### Veredicto

**Não entra.** Resolve um problema que decidimos não ter, e esbarra em dois que
temos. A decisão não é sobre a qualidade dele: tecnicamente é sério.

### O que ele é

Chromium **recompilado** com 73 patches em C++ — canvas, WebGL, áudio, fontes,
relato de GPU, WebRTC, timing de rede, sinais de automação — mais um wrapper fino
em Python/JS com a API do Playwright. Não é injeção de JavaScript nem flag de
linha de comando: a modificação está na fonte, que é o que faz a evasão
sobreviver a cada atualização do Chrome.

| | |
|---|---|
| Estrelas / forks | 31k / 2,6k |
| Versão | v0.5.10 (Chromium 151) |
| reCAPTCHA v3 | 0,9 — contra 0,1 do Playwright puro |
| Cloudflare Turnstile | passa |
| Licença do wrapper | MIT |
| Licença do binário | livre até v146; **v148+ exige assinatura Pro** |
| Camada grátis | **1 sessão concorrente** |

### Por que não entra — em ordem de peso

**1. Contradiz a regra nº 1 do agente.** A arquitetura inteira deste projeto é
*bateu numa barreira sensível → `request_takeover` → a pessoa assume*. É a
cláusula da doc do xAI que mais trabalho deu para virar código executável.
CloakBrowser é a doutrina oposta: contorne a barreira sem a pessoa.

Instalar os dois na mesma máquina não dá um agente mais capaz — dá um agente com
duas doutrinas em conflito, e o modelo escolhe a mais fácil. Foi exatamente por
isso que, ao esbarrar no anti-bot do Mercado Livre, o agente parou e nós
recusamos trocar user-agent, rotacionar proxy e resolver CAPTCHA.

**2. Quebra o modelo de N telas.** A camada grátis dá **uma** sessão concorrente,
e a v148+ exige assinatura. Este computador roda **uma tela por tarefa** por
decisão de arquitetura, e a trava por tela existe justamente para permitir várias
em paralelo. Bateria no teto na segunda tela.

**3. Não é o gargalo.** O que falta no nosso navegador não é evasão:

| Lacuna real | Onde |
|---|---|
| cookies não são compartilhados entre telas | §10 — divergência conhecida da doc |
| `browser.Execute` em 43,8% de cobertura | é o que puxa o pacote `tools` para 85,5% |

Trocar o binário não conserta nenhuma das duas.

**4. O take-over já resolve o caso legítimo, e melhor.** Você loga à mão uma vez,
a sessão fica no volume durável, e o agente usa depois — em qualquer visita
seguinte, sem repetir o login. É a cláusula *"Log in once"* da doc, implementada
e testada. CloakBrowser resolveria o mesmo caso por um caminho que a doc do Grok
Bot explicitamente **não** recomenda.

### O que dele seria aproveitável

O **humanize mode** — curva de Bézier no movimento do mouse, atraso por
caractere na digitação, rolagem com aceleração. Isso não é evasão: é interação
mais parecida com a de uma pessoa, e serve a qualquer site que quebre com clique
instantâneo ou preenchimento atômico. Daria para implementar dentro das
ferramentas `browser_*` sem trocar de binário e sem tocar na doutrina.

Não está feito porque ainda não medimos nada quebrando por causa disso — seria
solução procurando problema.

### O gatilho para revisitar

Se o objetivo deixar de ser *agente que trabalha sob supervisão* e passar a ser
*coleta em escala*. Aí a doutrina muda junto, o take-over deixa de fazer sentido,
e CloakBrowser passa a ser a escolha certa. Mas aí é outro produto.

---

# Loop engineering, porta HTTP e proatividade

> Trabalho de **30/08/2026**. Esta seção documenta o que mudou, por quê, e —
> principalmente — **os defeitos que a investigação desenterrou**. Vários eram
> reais e afetavam quem usava o sistema naquele dia.

## Por que este trabalho existiu

O computador funcionava, mas tinha **uma porta de entrada só** (`agentd -prompt`
por SSH) e **nunca falava primeiro**. Isso o mantinha como estudo: para virar
ferramenta, ele precisava de uma entrada que outros sistemas alcançassem e uma
saída pela qual avisasse sozinho.

A investigação para chegar lá encontrou três defeitos no laço — e o desenho veio
de três fontes: a decisão do dono, o levantamento de um assistente pessoal
anterior (PicoClaw, hoje desativado), e a pesquisa do estado da arte.

---

## 1. O vocabulário que faltava: loop × harness

O mercado separou duas coisas que costumavam ser tratadas como uma:

| | **Loop** | **Harness** |
|---|---|---|
| desenha | **comportamento** | **ambiente** |
| inclui | parada, retry, verificação, detecção de não-progresso, circuit breaker | ferramentas, sandbox, permissões, estado, mensagem de erro, observabilidade |

**A regra diagnóstica:** conserta mudando prompt, condição de parada ou retry →
é loop. Precisa mudar o que o agente **é capaz** de fazer ou ver → é harness.

E a regra de ordem: **agente não supervisionado → harness primeiro**.

O diagnóstico deste projeto, em 30/08:

| Harness | Estado |
|---|---|
| ferramentas, sandbox (`/workspace` × `/scratch`), permissões, estado durável, erro legível | ✅ forte |
| observabilidade | ⚠️ fraca |

| Loop | Estado |
|---|---|
| teto de iterações | ✅ |
| retry | ❌ **nenhum** |
| detecção de não-progresso | ❌ |
| circuit breaker | ❌ |

**O harness estava bom; o loop, quase vazio.** É o inverso do que a intuição
sugeria — e a ordem em que foi construído (harness primeiro) estava certa por
acidente, porque o agente roda sem supervisão.

---

## 2. Os três defeitos, confirmados no fonte

Nenhum era hipótese. Todos foram verificados lendo o código.

### 2.1 `-abandon` de tarefa pendente mentia

```go
if task.State == domain.StatePending {
    fmt.Printf("tarefa %s estava pendente e foi descartada\n", taskID)  // ← só imprime
} else if err := task.Fail(...)                                        // ← só o else muda o estado
```

O estado continuava `pending` → `Active()` conta `pending` → `ActiveTaskOnScreen`
filtra por `Active()` → **a tela seguia ocupada**. E o comando terminava
imprimindo `tela N liberada`.

**Mentira dupla**, e o sintoma era o pior possível: você abandonava, acreditava,
tentava de novo e levava *"a tela já tem uma tarefa ativa"* sem entender por quê.

O comentário no código explicava o raciocínio que levou ao erro — *"não há
transição de falha possível"* — e estava **certo** sobre a máquina de estados:
`Task.Fail` só aceitava `running` e `blocked`. A saída foi não fazer nada, em vez
de estender a transição.

### 2.2 Tarefa retomada perdia a resposta final

`Run` gravava a conversa ao concluir, com um comentário de quatro linhas
explicando que sem isso *"a resposta final do agente nunca chegaria ao disco"*.

`continueLoop` **não gravava**. O comentário existia só na metade que tinha a
gravação, e o defeito que ele descreve estava vivo na outra.

### 2.3 O turno que pedia take-over se perdia

A conversa é gravada no **início** de cada iteração. O resultado da ferramenta
que bloqueia é anexado depois disso, e o laço retornava chamando `persist`, que
grava **só a tarefa**.

O `Resume` recarregava a conversa do disco — sem o pedido de ajuda. **O agente
voltava do take-over sem saber por que tinha parado**, nem o que a pessoa foi
resolver.

### A causa comum

`Run` e `continueLoop` eram **o mesmo laço escrito duas vezes**, e já tinham
divergido. Por isso o passo zero foi unificá-los: sem um laço só, cada melhoria
seguinte seria escrita duas vezes e as cópias voltariam a divergir.

---

## 3. Não comprimir histórico — o que a pesquisa mudou

O instinto (e o projeto anterior) mandava comprimir o histórico preventivamente.
**Seria um erro**, e a razão é econômica:

> Com prompt caching, manter tudo é mais barato e lembra melhor. Comprimir
> reescreve o prefixo cacheado e faz pagar preço cheio para recomputar
> justamente o que a compressão tentaria economizar.

A xAI dá **75% de desconto** em token de entrada cacheado — US$ 0,50/M contra
US$ 2,00.

Então a compressão virou **reação, nunca prevenção**: só acontece quando o modelo
recusa explicitamente a janela, e uma vez só.

### O corolário que quase passou

`toolSpecs` iterava um **mapa**. Em Go, a ordem de iteração de mapa muda a cada
chamada — então a lista de ferramentas, que vai no prefixo do prompt, **mudava
entre iterações da mesma tarefa**.

Isso sozinho invalidava o cache. Cada iteração pagava preço cheio por um prompt
praticamente idêntico ao anterior, e ninguém veria o motivo na fatura.

Três linhas de `sort` resolveram. O canário mostra o defeito com precisão:
`"ordem mudou na posição 0: zeta vs xray"`.

---

## 4. Retry classificado

Três naturezas de falha, três tratamentos **opostos**:

| Natureza | Evidência | Tratamento |
|---|---|---|
| transitória | rede, tempo esgotado, 429, 5xx | repete a mesma chamada, backoff 2s/4s |
| janela estourada | 400/413 com vocabulário de contexto | encurta o histórico e refaz **uma** vez |
| permanente | 401, 404, esquema inválido | desiste na primeira |

**Classificação por código HTTP, não por texto.** O projeto de referência usava
nove `strings.Contains` sobre a mensagem porque o erro chegava achatado em
string; aqui o adaptador tem `resp.StatusCode` na mão. O corpo só entra como
evidência no **400**, que é ambíguo por natureza — cabe tanto *"seu JSON está
errado"* quanto *"seu prompt não cabe"*.

### A precedência importa, e tem teste em tabela

**429 e 5xx são transitórios mesmo quando o corpo menciona contexto.** Um
servidor sobrecarregado devolve qualquer texto, e tratar isso como janela
estourada faria o agente descartar histórico por causa de indisponibilidade
passageira — perde trabalho e não resolve nada.

### Duas decisões sobre a trava da tela

O retry acontece **com a trava na mão**. Daí:

- teto de **3 tentativas** (pior caso ~6 s de tela reservada por uma tarefa parada);
- backoff **cancelável**: `time.Sleep` puro seguraria a tela até o fim da espera
  mesmo depois de a tarefa ser cancelada — tela reservada por quem já desistiu.

E cancelamento **não é falha transitória**: se a pessoa apertou Ctrl+C, repetir
gasta token contra a vontade dela.

---

## 5. Proatividade: o agente fala primeiro

### O requisito duro, e de onde veio

No PicoClaw, o transporte de saída disputava a mesma conexão do de entrada (uma
conexão por dispositivo). Para o agendador conseguir avisar, ele **derrubava o
serviço**:

```bash
systemctl stop picoclaw
sleep 2
wa-send -to "$RECIPIENT" -text "$FINAL"
sleep 1
systemctl start picoclaw
```

A cada 30 minutos, das 6h às 22h. Isso é ~3 s de indisponibilidade por
notificação.

**Requisito derivado:** o canal de saída **não pode depender da conexão de
entrada**.

### A solução: spool + drenador

```
tarefa para  ──▶  Publish()  ──▶  events.jsonl   (escrita local, retorna)
                                       │
                        agentd -notify-drain     (processo separado, por timer)
                                       │
                                    webhook
```

`Publish` **grava e retorna** — não envia nada. Quem entrega é outro processo.
O requisito fica atendido **por construção**, não por disciplina: matar a sessão
SSH que iniciou a tarefa não mata a entrega, porque a entrega nunca esteve nela.

E há um segundo motivo: um webhook lento dentro da tarefa **seguraria a trava da
tela** enquanto o destino pensa.

### Avisar é efeito colateral

`publish` **não devolve erro**, e esse é o contrato. Um destino fora do ar não
pode transformar tarefa concluída em tarefa falhada — o trabalho foi feito e o
disco já registrou.

Mas a falha não é engolida: vira **nota de sistema no histórico**. Engolir faria
quem lê a conversa concluir que nunca houve o que avisar.

### Decisões pequenas com motivo

| Decisão | Por quê |
|---|---|
| `O_APPEND`, uma linha por evento | dois agentes em telas diferentes se sobrescreveriam, e o desaparecido seria o de quem escreveu primeiro — a tarefa que espera há mais tempo |
| linha corrompida é **pulada** | uma queda no meio de uma escrita não pode impedir a entrega de todos os outros avisos |
| `Clear` **trunca**, não apaga | preserva permissão e o descritor de quem já o tem aberto |
| fila só é limpa se **tudo** foi entregue | limpar após entrega parcial perderia o restante; a consequência aceita é a oposta — aviso repetido incomoda, aviso perdido deixa uma tela travada |
| só `blocked` e `failed` são enfileirados | avisar de tudo ensina quem recebe a ignorar, inclusive o take-over |
| sem `-webhook`, o drenador só **lista** | é o primeiro comando de quem desconfia que um aviso sumiu; consumir a fila ali destruiria a evidência |

### Um caminho que avisava ninguém

O teste de falha encontrou uma lacuna real: **erro do modelo encerrava a tarefa
por um caminho que não passava pelo `settle`**. Ela morria em silêncio — gravava
o estado e ninguém era avisado, justamente no caso em que alguém precisa saber.

---

## 6. Paralelismo DECLARADO

Paralelizar todas as ferramentas seria o caminho óbvio e **estaria errado**:

- as ferramentas do navegador falam com a **mesma aba** do Chrome;
- o shell mexe no mesmo `/workspace`;
- o take-over muda o estado da tela.

Duas ações simultâneas nesses recursos **não falham — fazem a coisa errada, em
silêncio**. É exatamente o modo de falha que motivou a trava de uma tarefa por
tela, e paralelizar às cegas o reintroduziria por dentro.

### `ToolSpec.Concurrent`, padrão falso

Quem marca verdadeiro assume três compromissos, escritos no comentário do campo:
não guardar estado entre chamadas, não tocar em recurso compartilhado, e honrar
o cancelamento do contexto.

Hoje **só a chamada de API por conector** se qualifica. Como o zero-value é
seguro, nenhuma ferramenta existente precisou mudar — e todo turno com take-over
roda exatamente como antes.

### Tudo-ou-nada por turno

Basta uma ferramenta com estado para o turno inteiro rodar em série. Particionar
em blocos criaria um escalonador com espaço combinatório de testes, e o caso real
que paga a conta (dois conectores no mesmo turno) já é atendido.

### Ordem do modelo, não de término

O histórico é o que o modelo relê na iteração seguinte. **Ordem que muda a cada
execução torna a conversa irreprodutível**, e nenhum defeito daí se reproduz duas
vezes igual.

Cada goroutine escreve só na própria posição do vetor; a conversa e a tarefa são
mutadas depois, em série, por um consumidor único.

### Bloqueio simultâneo: duas decisões

- **Vence o primeiro na ordem do modelo**, deterministicamente. O segundo recebe
  uma explicação, em vez do *"pedido recusado"* que a transição inválida
  produziria — que sugeriria má-formação onde só houve concorrência.
- **As irmãs nunca são canceladas.** Matar uma que já disparou um POST produziria
  efeito no mundo **sem registro no histórico** — e o histórico é o que alguém lê
  para saber o que a máquina fez.

### `-race` no gate

Obrigatório a partir do momento em que o laço tem goroutines. Um gate sem ele
deixa corrida de dados passar **verde** — e corrida aqui não trava, faz a coisa
errada calada.

**Ele pagou o próprio custo na primeira execução** — ver §7.3.

---

## 7. A porta HTTP

```
POST /tasks               201 + Location
GET  /tasks/{id}          estado + a RESPOSTA da tarefa
POST /tasks/{id}/resume   202
POST /tasks/{id}/abandon  200
GET  /health              200, sem token
```

Sem dependência de roteador: `net/http` do Go 1.22+ roteia por método e parâmetro
de caminho. A superfície de terceiros deste projeto é um ativo — são três
dependências diretas ao todo.

### 7.1 O defeito número um deste tipo de adaptador

O contexto de uma requisição **morre quando o handler retorna**. Se a goroutine
derivasse dele, a tarefa morreria na primeira chamada ao modelo — com o cliente
já tendo recebido *"criada com sucesso"* e a tarefa marcada como falha por
`context canceled`.

Falha **silenciosa** e difícil de atribuir: tudo parece ter funcionado.

Por isso a goroutine deriva do contexto do **processo**. O canário reprova com a
mensagem exata:

```
a tarefa herdou o contexto da REQUISIÇÃO e morreu com ela: context canceled
```

### 7.2 409, nunca fila

Três checagens **sob o mesmo mutex**, porque cada uma enxerga o que as outras não
veem:

| Fonte | O que só ela pega |
|---|---|
| registro em memória | o que **este** processo roda |
| disco | tarefa bloqueada de um boot anterior, e tarefa criada pelo CLI |
| trava (sonda) | o CLI rodando **agora** em outro processo, cuja prova de vida não está no disco |

O canário abre uma janela de 1 ms entre checar e registrar, e o teste reprova com
`"exatamente uma devia entrar, entraram 8"` — oito tarefas na mesma tela,
disputando o mesmo teclado. Fila esconderia isso; a recusa não.

**A trava é sondada, não segurada.** `flock` é por descritor aberto, e segurá-la
para entregar ao laço faria o laço travar contra a própria sonda. Sobra uma
janela de microssegundos cujo desfecho é uma tarefa **falha e visível**, não
enfileiramento silencioso. Documentado, não escondido.

### 7.3 A corrida de dados que o `-race` encontrou

Na primeira execução do gate com `-race`, ele acusou uma corrida de **produção**:

```
goroutine da tarefa:  Task.Start()  ── escreve o estado
handler HTTP:         describe()    ── lê o mesmo Task para a resposta
```

`Supervisor.Start` devolvia o **ponteiro**, e o handler o serializava enquanto a
goroutine já o estava mutando. Duas mãos no mesmo objeto, sem sincronização.

O sintoma em produção não seria um erro: seria uma **resposta com o estado meio
escrito**, de vez em quando, sem nada no log.

E a correção teve uma sutileza: **copiar depois do disparo não resolve** — a
goroutine já começou. A primeira tentativa fez exatamente isso e o detector
continuou acusando. A cópia precisa vir **antes** do `spawn`.

### 7.4 Reconciliação no boot

O oráculo é a **trava**: ela morre com o processo, o estado em disco não. Logo,
tarefa marcada como ativa cuja tela está destravada é cadáver.

Duas decisões que o teste protege:

- **BLOQUEADA NÃO É CADÁVER.** É o estado que a documentação exige quando aparece
  senha, 2FA ou CAPTCHA, e é durável de propósito: alguém precisa agir.
  Convertê-la em falha jogaria fora o trabalho e faria o take-over deixar de
  existir na prática. O que morreu foi o **aviso na tela**, que era um processo —
  ele é redesenhado, senão a tela parece ociosa enquanto está reservada.
- **Trava recusada é prova de vida**: há outro processo trabalhando ali, e
  matá-lo pelo disco destruiria trabalho em curso.

`Reconcile` roda **antes** de a porta aceitar conexão. Com o servidor no ar, ele
mataria uma tarefa recém-criada que ainda não tomou a trava.

### 7.5 O token falha FECHADO

Recusa arquivo ausente, permissão frouxa (inclusive só para o grupo) e token
curto. **Uma porta que sobe sem autenticação porque o arquivo sumiu é o pior
desfecho possível**: tudo funciona, ninguém percebe, e a máquina fica aberta com
acesso a shell, navegador e credenciais de conta.

As mensagens dizem **como** consertar (`chmod 600`, o script de geração), com
teste para isso — descobrir o procedimento no meio de um incidente é tarde
demais.

Detalhes:

- comparação de **tempo constante**: `==` sai no primeiro byte diferente, e a
  diferença entre "errou no byte 1" e "errou no byte 30" é medível pela rede;
- `127.0.0.1:8787` por padrão, IP da malha depois — **nunca `0.0.0.0`**;
- saúde fica **fora** da autenticação: autenticá-la obrigaria o supervisor de
  processo a carregar o segredo só para provar que a porta responde;
- corpo limitado a 64 KB: sem teto, uma requisição enorme consome memória do
  processo que segura as telas, e derrubá-lo não exigiria nem autenticação.

### 7.6 O CLI continua funcionando

`agentd -prompt` não mudou. As duas entradas passaram a usar o **mesmo**
`Lifecycle` e a **mesma** fábrica de agente — duplicar as regras é como as pontas
divergem, e a que diverge em silêncio é sempre a que ninguém roda. Foi exatamente
assim que o abandono de tarefa pendente passou a mentir.

---

## 8. Comandos novos

```bash
# a porta HTTP
agentd -serve                          # 127.0.0.1:8787
agentd -serve -listen 100.x.y.z:8787   # na malha, NUNCA 0.0.0.0
agentd -serve -token-file <caminho>    # padrão: <state>/api-token

# proatividade
agentd -notify-drain                   # lista o que está pendente
agentd -notify-drain -webhook <url>    # entrega e limpa a fila
```

---


## 11. Operação: como a porta sobe de verdade

Três peças, nesta ordem — e nenhuma delas é opcional.

### 11.1 As credenciais, do cofre para a máquina

```bash
task serve-setup
```

Instala duas coisas, e **as duas em arquivo `0600`, nunca em `Environment=`**:

| Arquivo | O quê | Por que arquivo |
|---|---|---|
| `/workspace/agent/xai.env` | chave do modelo | `systemctl cat` e `/proc/<pid>/environ` expõem o ambiente de um processo; e rotacionar passaria a exigir editar a unidade |
| `/workspace/agent/api-token` | token da porta | idem, mais o fato de a porta **falhar fechada** sem ele |

O token é **gerado**, nunca digitado: `openssl rand -hex 32`. Token digitado à mão
é adivinhável. Ele nasce no `openssl`, vai para o `pass` e para o droplet por
stdin, e o arquivo temporário morre em `shred` — nunca é impresso.

⚠️ A chave do modelo mudou de lugar por necessidade: até aqui ela viajava na
**linha do SSH a cada invocação**, o que funciona para um comando pontual e não
funciona para um serviço — o systemd sobe o processo sem ninguém para passá-la.

### 11.2 As unidades vivem no `cloud-init`, não no droplet

```
/etc/systemd/system/agentd-api.service      a porta HTTP
/etc/systemd/system/agentd-notify.service   a entrega dos avisos
/etc/systemd/system/agentd-notify.timer     a cada minuto
```

**Isto não é preferência de organização.** `task update` destrói a máquina e
remonta só o volume: uma unidade escrita à mão no droplet **some no primeiro
update**, o serviço não volta, e ninguém entende por quê.

Detalhes que importam em cada uma:

| Diretiva | Motivo |
|---|---|
| `RequiresMountsFor=/workspace` | sem isto o systemd sobe o serviço antes da montagem, e ele falha lendo um diretório vazio |
| `TimeoutStopSec=40` | o encerramento limpo cancela as tarefas em voo, elas gravam o estado e soltam a trava — 40 s dá folga antes do SIGKILL |
| `Restart=on-failure` | e não `always`: um serviço que sai limpo saiu por decisão |
| `EnvironmentFile=-/etc/agentd/notify.env` | o `-` torna opcional; sem destino, o drenador só lista. **Fora de `/workspace`** de propósito: o destino dos avisos não pode ficar num diretório que o modelo alcança |
| `AccuracySec=10s` no timer | sem isto, um drenador lento acumularia execuções sobrepostas disputando a mesma fila |

**Nenhuma é habilitada por padrão.** A porta exige as duas credenciais
instaladas antes; subir sem elas produziria um serviço em falha reiniciando para
sempre. Quem habilita é `task serve-enable`.

### 11.3 O deploy reinicia o serviço

`16-deploy-agent.sh` ganhou um passo: depois de instalar o binário, ele
**reinicia a porta se ela estiver no ar**.

Sem isso, o binário novo fica no disco e o serviço continua rodando o velho — o
deploy reporta sucesso e nada muda no comportamento. É o modo de falha mais
confuso possível: o código está certo, o teste passa, e a máquina insiste no bug
que você acabou de corrigir.

### 11.4 O que ainda não foi exercitado na máquina real

Honestidade sobre o alcance do que está testado:

| | Estado |
|---|---|
| a porta, os handlers, o supervisor, o token | ✅ testados, com `-race` |
| `-serve` subindo e recusando sem token | ✅ provado pelo binário, localmente |
| as unidades systemd | ⚠️ **nunca rodaram** — o droplet foi destruído ao fim da sessão |
| reconciliação depois de `kill -9` | ⚠️ tem teste em processo, falta na máquina |
| aviso entregue com a sessão SSH morta | ⚠️ idem |

As duas últimas são justamente o que teste em processo **não alcança**: "o flock
morre com o processo" é garantia do sistema operacional, e um teste em processo
não consegue exercitá-la porque o descritor é dele.

Ao subir o droplet de novo, a sequência é:

```bash
task up && task deploy && task serve-setup && task serve-enable
task serve-status        # confirma pelo efeito
```

## 9. Números medidos, 30/08/2026

| | |
|---|---|
| Cobertura total | **91,1%** (piso 90%) |
| Domínio | **100%** (exigência 100%) |
| Detector de corrida | `-race` no gate |
| Dependências diretas | 3 |
| Desconto de cache da xAI | 75% (US$ 0,50/M contra 2,00) |
| Defeitos de produção corrigidos | 4 (3 no laço + 1 corrida de dados) |

---

## 10. O que a sessão ensinou sobre o próprio processo

**O gate cobrou quatro vezes, e nas quatro eu teria deixado passar:**

1. um teste que virou lento (6 s) porque o retry passou a repetir;
2. o domínio em 99,3% por um ramo defensivo descoberto — exatamente o tipo de
   linha que alguém remove por parecer código morto;
3. `LastAnswer` e `AddSystemNote` entrando sem teste próprio;
4. o total caindo para 89,3% quando o adaptador HTTP entrou sem teste.

**O canário pegou um teste decorativo:** `TestDelegateRejectsEmptyTask` passava
com a validação removida — a falha vinha do arquivo de credencial ausente, não da
tarefa vazia. O teste verificava `Failed` sem verificar o **motivo**.

**Um sweep de renomeação vazou duas vezes:** para dentro de comentário
(`"a taskText MISTA"`) e para dentro de string (`"agente-fakeAgent"`). Em Go,
variável dentro de aspas não existe — mas comentário de fim de linha existe, e é
por ele que o vazamento entra. A terceira versão separa código, comentário e
literal.
