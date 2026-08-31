# O percurso de uma tarefa, do pedido ao arquivo gravado

Este documento segue **uma tarefa real** do começo ao fim, mostrando cada ponto
de controle no caminho. Os outros documentos explicam cada peça; aqui está como
elas se encaixam.

A tarefa que vamos seguir:

```bash
agentd -screen 1 -prompt "@digitalocean /web-search compare o preço do meu droplet
                          com a média de mercado e escreva o resultado em /workspace/relatorio.md"
```

Ela toca **tudo**: conector com credencial, habilidade, navegação, escrita de
arquivo, e os guardrails no meio.

---

## Panorama: as sete etapas

```
  PEDIDO                    ┌─ trava de tela ──────┐
    │                       │  409 se ocupada      │
    ▼                       │  429 se máquina cheia│
  1. ADMISSÃO ──────────────┘                      │
    │                                              │
    ▼                                              │
  2. MONTAGEM DO PROMPT                            │
    │  habilidade + lições + conectores anexados   │
    ▼                                              │
  3. LAÇO ◄─────────────────────────┐              │
    │  detectores ANTES do modelo   │              │
    ▼                               │              │
  4. MODELO responde                │              │
    │  custo somado, turno contado  │              │
    ▼                               │              │
  5. FERRAMENTA executa             │              │
    │  rebaixada para `agent`       │              │
    ▼                               │              │
  6. RESULTADO ─────────────────────┘              │
    │  redigido, contado, registrado               │
    ▼                                              │
  7. DESFECHO ─────────────────────────────────────┘
       done · blocked · failed
```

---

## 1. Admissão: três recusas antes de começar

O pedido chega pela porta HTTP (`api/server.go`) ou pelo CLI. Antes de virar
tarefa, passa por três portões, **nesta ordem** — e a ordem é o que faz a
mensagem de erro ser útil:

| Portão | Recusa | Código | Como se resolve |
|---|---|---|---|
| máquina cheia | 4 tarefas já rodando | `429` | esperar |
| tela ocupada | outra tarefa naquela tela | `409` | retomar ou abandonar **aquela** tarefa |
| pedido malformado | prompt vazio, tela fora de 1..9 | `400` | corrigir o pedido |

**Por que o teto global vem primeiro:** recusar por "tela ocupada" uma tarefa que
a máquina não comportaria mandaria o cliente tentar outra tela — e a próxima
falharia igual, com a mensagem errada nas duas vezes.

A ocupação da tela é conferida em **três fontes** (`api/supervisor.go`), porque
cada uma enxerga o que as outras não veem:

1. o registro em memória — o que **este** processo roda;
2. o disco — tarefa bloqueada de um boot anterior, ou criada pelo CLI;
3. a trava (`flock`) — o CLI rodando **agora**, em outro processo.

> Detalhe: a trava é tomada e solta **como sonda**. Segurá-la e entregar ao laço
> não funciona — `flock` é por descritor aberto, e o laço colidiria com a
> própria sonda.

## 2. Montagem do prompt: o que o modelo realmente recebe

O texto do pedido **não** é o que chega ao modelo. Três coisas se somam:

```
prompt de sistema  =  instruções compiladas
                    + lições de guardrails.md          ← se houver
prompt do usuário  =  pedido sem os marcadores
                    + conteúdo das habilidades         ← se houver
ferramentas        =  as sempre disponíveis
                    + as dos conectores anexados com @
```

**Os marcadores somem do texto.** `@digitalocean` e `/web-search` são instrução
para o agente, não para o modelo — deixá-los confundiria o objetivo.

⚠️ Um **caminho de arquivo não vira habilidade**: `/workspace/projects` continua
sendo um caminho. A distinção está em `domain/connector.go`, e existe porque a
primeira versão anexava uma habilidade chamada "workspace" *e* comia o caminho.

⚠️ As lições entram **inteiras**, não como caminho de arquivo. É a diferença
central em relação ao [ralph](https://github.com/iannuttall/ralph), de onde a
ideia veio: lá o prompt recebe o caminho e pede que o modelo leia; se ele não
ler, nada acontece.

**E aqui a redação é armada**: os segredos dos conectores anexados são
registrados na conversa, para sumirem se reaparecerem em qualquer saída.

## 3. O laço: detectores antes de gastar o turno

`service/agent.go`, função `iterate`. A cada volta, **antes** de chamar o modelo:

| Detector | Dispara quando | Resultado |
|---|---|---|
| turnos acumulados | 180, somando retomadas | `blocked` |
| tempo de parede | 80% das 2 h | `blocked` |

Depois da resposta, mais dois:

| Detector | Dispara quando | Resultado |
|---|---|---|
| custo acumulado | US$ 3,00, somando retomadas | `blocked` |
| resposta truncada | `finish_reason: "length"` | `blocked` |

**Por que antes e não depois:** parar depois de gastar o turno seria pagar
exatamente o que o teto existe para evitar.

**Por que `blocked` e não `failed`:** o guardrail *para* a tarefa, não a joga
fora. O trabalho e a tela ficam, e a pessoa decide se retoma. Se encerrasse,
parar cedo custaria tudo o que já foi feito — e a primeira reação seria
desligá-lo.

## 4. O modelo responde, e a conta é feita

Três coisas acontecem com a resposta, nesta ordem:

1. **o turno é contado** — antes da chamada, não depois: um turno que falha no
   meio consumiu recurso do mesmo jeito;
2. **o custo é somado** — `(prompt − cache) × preço + cache × preço_cache +
   saída × preço_saída`, com a faixa escolhida pelo tamanho do prompt;
3. **a atividade é registrada** em `activity.log`, com tokens, cache, custo e
   duração.

⚠️ O **cache custa 4× menos**, e este agente o usa de propósito — a ordem estável
das ferramentas existe para isso. Ignorá-lo superestimaria a conta em 4×.

Se a resposta não pede ferramenta, a tarefa termina — **exceto** se veio
truncada. Um `finish_reason: "length"` chega sem chamada de ferramenta, e antes
era tratado como conclusão bem-sucedida, com a resposta pela metade.

## 5. A ferramenta executa, rebaixada

Aqui está a fronteira de segurança mais importante do sistema.

```
  agentd (usuário `agentd`)          ← tem o cofre, a chave do modelo, o token
    │
    │  sudo -n -u agent --
    ▼
  ferramenta (usuário `agent`)       ← não lê o cofre, não vira root
```

O que cada ferramenta atravessa antes de rodar:

| Ferramenta | Verificação |
|---|---|
| `shell` | argumentos com campo desconhecido são **recusados** |
| `browser_*` | fala só com `127.0.0.1:922N`, a porta de depuração local |
| conector | parâmetro fora do schema recusado; **IP de destino validado no discador** |
| `delegate_to_code` | runner do catálogo fechado, credencial e `HOME` próprios |

⚠️ O discador do conector valida o IP **no momento de abrir o socket**, depois
da resolução de DNS e de cada redirect. Validar a URL cadastrada não bastaria:
rebinding, redirect e registros múltiplos escapam de qualquer checagem anterior.

## 6. O resultado volta, e passa por três filtros

```
saída da ferramenta
   │
   ├─ REDAÇÃO      segredo de conector vira [REDIGIDO]
   ├─ TRUNCAMENTO  8 KB no shell, 6 KB na delegação
   └─ DETECÇÃO     3 falhas idênticas seguidas → blocked
   │
   ▼
histórico da conversa  → volta ao modelo na próxima iteração
```

**A detecção de laço** compara `(ferramenta, argumentos)` por hash. Qualquer
sucesso, ou uma falha diferente, zera a contagem — é o que separa "insiste no
mesmo erro" de "erra enquanto explora".

Quando ela dispara, a lição vai para `guardrails.md` e entra no prompt de **toda
tarefa futura**. É o único dos detectores que ensina algo reaproveitável: "você
gastou demais" não serve para a próxima.

## 7. O desfecho

Toda tarefa passa por `settle`, que é o único ponto de saída:

```
conversa gravada → tarefa gravada → progresso anotado → evento publicado
```

**A ordem importa.** O durável vem antes do aviso: se o processo morrer no meio,
quem consulta o disco vê a verdade. O inverso avisaria "concluída" com o disco
dizendo outra coisa.

| Estado | Significa | O que fazer |
|---|---|---|
| `done` | terminou | ler a resposta em `progress.md` ou na conversa |
| `blocked` | parou e espera uma pessoa | `-resume` depois de agir, ou `-abandon` |
| `failed` | erro que o laço não contornou | ver `errors.log` |

Só `blocked` e `failed` viram **aviso** na fila — avisar de tudo ensina quem
recebe a ignorar, inclusive o pedido de take-over, que é o único que trava a
tela até alguém agir.

---

## Se o processo morrer no meio

O `kill -9` não avisa ninguém. A recuperação acontece no **boot seguinte**,
antes de a porta abrir (`service/lifecycle.go`):

1. lê as tarefas marcadas como ativas no disco;
2. para cada uma, tenta tomar a trava da tela;
3. **conseguiu** → não há processo vivo: é cadáver, vira `failed` **e avisa**;
4. **não conseguiu** → há processo de verdade rodando: não toca.

⚠️ Tarefa `blocked` **não** é cadáver. A tela é redesenhada e o estado
preservado — convertê-la em falha destruiria o take-over na prática.

⚠️ A reconciliação roda **antes do listener**. Com a porta já aberta, ela mataria
uma tarefa criada há um instante que ainda não tomou a trava — e ela pareceria
ter falhado sozinha, sem motivo visível.

---

## Onde olhar quando algo dá errado

| Sintoma | Primeiro lugar |
|---|---|
| a tarefa parou e não sei por quê | `progress.md` — uma linha por desfecho |
| demorou ou custou demais | `activity.log` — turnos, ferramentas, tokens, custo |
| uma ferramenta falhou | `errors.log` — com a contagem de repetição |
| quero ver o que o modelo pensou | `conversations/<id>.json` |
| a tela está em 409 e não devia | `locks/` — dono errado põe toda tela em conflito |
| o agente repete um erro antigo | `guardrails.md` — pode haver lição obsoleta |

```bash
task answers          # a RESPOSTA das últimas tarefas, não só o estado
task serve-logs       # o log do serviço
task validate         # 11 seções: units, telas, portas, boot
```

---

## Os documentos, e quando ler cada um

| Documento | Leia quando |
|---|---|
| este | quer entender **como as peças se encaixam** |
| [`GUARDRAILS.md`](GUARDRAILS.md) | vai mexer nos limites, ou um deles disparou |
| [`SECURITY.md`](SECURITY.md) | vai "endurecer" algo — leia **antes** |
| [`STATE-FILES.md`](STATE-FILES.md) | precisa saber o que é um arquivo em `/workspace/agent` |
| [`EXTENDING.md`](EXTENDING.md) | vai criar conector, habilidade ou runner |
| [`TEST-MAP.md`](TEST-MAP.md) | quer saber se algo tem teste, e qual |
| [`README.md`](../README.md) | está começando, ou quer o histórico das decisões |
