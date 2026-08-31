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
| **custo acumulado da tarefa** | **US$ 3,00** | `AGENTD_MAX_COST_USD` |
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

### Teto global de tarefas simultâneas

A trava de tela garante uma tarefa **por tela** — e são nove telas, o que nunca
foi um teto de máquina. Nove tarefas simultâneas significam nove navegadores e,
no pior caso, nove delegações de US$ 5,00 cada.

**Quatro** é medido nesta máquina, não escolhido por gosto:

| | |
|---|---|
| memória total | 3.919 MB, ~2.600 livres em repouso |
| Chrome por tela | ~370 MB (medido com duas telas de pé) |
| `agentd` | 282 MB |
| CPU | 2 vCPU |

Quatro tarefas com navegador dão ~1,5 GB de Chrome, que cabe com folga. Nove
dariam 3,3 GB e estourariam — e o modo de falha do estouro é o pior: o OOM
killer escolhe a vítima, e ela costuma ser o processo maior, que é o `agentd`.

Ajustável por `AGENTD_MAX_CONCURRENT_TASKS`, porque num droplet maior o teto
certo é outro.

**Conta só tarefa em EXECUÇÃO.** Tarefa bloqueada esperando uma pessoa não
ocupa vaga: ela não gasta CPU nem token, e contá-la faria o take-over numa tela
impedir trabalho em outra.

#### 429, e não 409

A distinção é para quem chama, e a máquina devolve os três códigos:

| Código | Situação | Como se resolve |
|---|---|---|
| `201` | tela livre, máquina com vaga | — |
| `409` | **aquela tela** está ocupada | retomar ou abandonar a tarefa que a segura |
| `429` | a **máquina** está cheia | esperar |

Misturar os dois faria o cliente tentar abandonar uma tarefa que não é a causa,
ou trocar de tela — e a próxima falharia igual, com a mensagem errada nas duas
vezes. Por isso o teto global é conferido **antes** do teste de tela.

Medido na máquina: `{"error":"tarefas demais rodando ao mesmo tempo: 4 rodando,
teto 4","hint":"espere uma tarefa terminar..."}`

### Teto de custo

Em dólares, não em tokens: token não se compara entre modelos, e o limite que
importa a quem paga é o da fatura.

**A tabela de preços mora em `/workspace/agent/pricing.json`, não no binário.**
Preço envelhece, e tabela compilada só se corrige recompilando — uma tabela
velha é pior que nenhuma, porque o teto passa a cortar no lugar errado e o
número parece medido. Cada entrada carrega a origem e a data.

Três coisas que a conta precisa acertar, e que uma multiplicação ingênua erra:

| | Por quê |
|---|---|
| **cache custa 4× menos** | US$ 0,50 contra 2,00 por 1M no grok-4.6. Este agente usa cache de propósito — a ordem estável das ferramentas existe para isso —, e ignorá-lo superestimaria a conta em até 4×, parando a tarefa cedo demais |
| **acima de 200k de prompt o preço DOBRA** | entrada e saída. Um histórico longo cruza essa linha sem avisar, e uma tabela de preço único erraria por 100% justamente nas tarefas caras |
| **`cached` está contido em `prompt`** | não somado. Somar contaria o mesmo token duas vezes |

**Modelo sem preço não bloqueia — e isso é deliberado.** `0, false` significa
"não sei", não "de graça": tratar os dois igual faria um modelo recém-cadastrado
rodar sem teto sem nada indicar. Os tokens continuam somados, então dá para
descobrir depois quanto ele andou custando.

Conferido na máquina, com a conta batendo à mão: `tokens=2505/3 cache=512
custo=US$0.0043` — (2505−512)×2,00 + 512×0,50 + 3×6,00, por milhão.

O paralelo que justifica o teto existir: a delegação ao agente de código já
rodava com `--max-budget-usd 5.00`. Era o **único** teto de dinheiro do sistema —
o agente delegado tinha orçamento e o principal não.

## Multi-runner na delegação

`delegate_to_code` aceita um `runner` opcional, resolvido contra
`/workspace/agent/runners.json` (`agentd:agent 0640`). Sem ele, Claude Code como
sempre.

O ralph faz o mesmo com `AGENT_CMD` — e com `eval` sobre uma string de shell.
Aqui o comando é **vetor**, vai direto para `exec.Command`, e o catálogo recusa
qualquer entrada cujo binário seja um interpretador (`sh`, `bash`, `env`,
`xargs`). O motivo é concreto: com shell no meio voltam `;`, `&&` e `$(...)`, e
com eles `sudo` — que desfaz o rebaixamento do modelo para o usuário `agent`.

Cadastrados: `claude` e `opencode` **funcionando** (provados na máquina),
`codex` instalado mas sem acesso ao endpoint que o CLI usa, e `droid`/`kiro` não
instalados. O estado de cada um está em [`EXTENDING.md`](EXTENDING.md).

Os não instalados ficam no catálogo de propósito — pedir um deles falha
**nomeando o binário que falta**, e a mensagem vira a documentação de instalação.

Cada runner tem a **sua** credencial (`env_file`) e o **seu** `HOME`: um agente
de código executa comando arbitrário por desenho, e a chave que ele alcança é a
chave que pode sair da máquina.

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

## Argumento do modelo: campo desconhecido é recusado

Fechado em 31/08/2026. Havia duas portas, e as duas ignoravam em silêncio o que
não conheciam.

**Ferramentas nativas** (shell, browser, takeover, delegate) decodificavam com
`json.Unmarshal`, que descarta campo desconhecido. Um `{"comand":"ls"}` — o erro
de digitação que um modelo comete — decodificava sem erro, deixava o campo certo
vazio, e a ferramenta respondia **"comando vazio"**. A mensagem mandava
investigar a coisa errada, e o modelo tendia a repetir a chamada em vez de olhar
o nome do campo. Agora usam `decodeArgs`, com `DisallowUnknownFields`.

**Conector HTTP** é o caso mais grave, porque falha sem parecer falha: o que
sobra depois de preencher o caminho vira **query string**. Um `{"stat":"opened"}`
em vez de `state` não dava erro — era anexado à URL, a API remota o ignorava, e
a listagem voltava **sem filtro, com todos os itens**. O modelo concluía que o
filtro não funciona na API. Agora o parâmetro é conferido contra as `properties`
que o manifesto declara, e a recusa lista os aceitos.

Duas decisões que evitam o alarme falso:

| | |
|---|---|
| esquema **sem** `properties` pula a validação | vazio é "não sei o que é válido", não "nada é válido" — recusar quebraria manifesto antigo |
| esquema **malformado** não barra a chamada | quem o escreveu foi o operador; virar recusa que o MODELO recebe o faria gastar turnos consertando o que não alcança |

Não se valida tipo nem obrigatoriedade: disso a API remota reclama, com mensagem
melhor que a nossa. O que ela **não** tem como reclamar é do parâmetro que não
conhece — ela o ignora. É essa lacuna, e só ela.

### A prova de falha reprovou o TESTE, não o código

Vale registrar porque é o modo de falha que este projeto já cometeu antes:
desarmar `checkParams` no `httptool` deixava todos os casos passando, porque
todos chamavam a função **direto**. Testar a função não prova que alguém a
chama — foi assim que `RecordProgress` ficou escrito, testado e nunca invocado,
com o arquivo em 0 bytes na máquina.

O caso que fechou isso passa pelo `Execute` de verdade e confere que a
requisição **não saiu**: `TestValidationIsWiredIntoTheTool`.

> Para ver **onde estes detectores entram no percurso de uma tarefa** — do pedido ao arquivo gravado —, leia [`TASK-LIFECYCLE.md`](TASK-LIFECYCLE.md).

## O que continua de fora

