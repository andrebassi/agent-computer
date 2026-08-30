# Arquitetura do agent-computer, ponta a ponta

> Documento técnico e didático. Explica **o que** foi construído, **por que** cada
> peça existe, e **como** cada cláusula da documentação do Grok Bot virou código
> executável e testado.
>
> Fonte que estamos reproduzindo:
> <https://docs.x.ai/grok-bot/computer-and-apps> (atualizada em 11/08/2026).
> Auditoria cláusula por cláusula, com placar: [`fidelity.md`](fidelity.md).

---

## 1. O que este projeto é, em uma frase

Um **computador em nuvem persistente com tela própria, dirigido por um agente
autônomo**, montado em infraestrutura própria — reproduzindo o modelo que a xAI
descreve para o Grok Bot, que é produto fechado e não tem API nem pacote
instalável.

Não é um clone do Grok Bot. É a mesma **arquitetura**, construída do zero, com as
mesmas garantias, e com as divergências registradas onde elas existem.

---

## 2. O modelo que estamos reproduzindo

Antes de ver o código, vale entender o desenho que a documentação descreve,
porque quase toda decisão daqui decorre dele.

### 2.1 A ideia central: o computador não é seu laptop

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

### 2.2 Um computador por conta, N telas

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

### 2.3 O que sobrevive e o que não

A documentação separa dois mundos:

| Durável (sobrevive a update e recovery) | Descartável (some) |
|---|---|
| `/workspace` | diretórios temporários |
| estado do navegador, sessões logadas | pacotes instalados à mão |
| | estado de aplicação não salvo |

Essa fronteira é a base dos três verbos de manutenção. Sem ela declarada, tudo
vira igualmente frágil ou igualmente permanente — e nos dois casos a manutenção
fica impossível.

### 2.4 O handoff humano

Quando o agente esbarra em senha, verificação em duas etapas, CAPTCHA, cobrança
ou um site que exige uma pessoa, ele **para e chama você**. Você resolve só aquele
passo e devolve o controle.

O que a documentação proíbe é o oposto: tentar contornar. Um agente que tenta
adivinhar senha ou resolver CAPTCHA sozinho costuma derrubar a sessão do site e,
pior, produz ações que ninguém autorizou.

---

## 3. Panorama: as duas metades

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

## 4. A metade 1: o computador

### 4.1 Por que o droplet é descartável

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

### 4.2 As telas

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

### 4.3 Acesso: nada é publicado

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

### 4.4 Os três verbos

| Verbo | Comando | Preserva | Descarta | Como funciona |
|---|---|---|---|---|
| **Update** | `task update` | `/workspace`, perfis, sessões | `/scratch`, pacotes manuais, sistema | destaca o volume → destrói o droplet → cria outro com imagem nova → remonta |
| **Reset** | `task reset` | só o que está no snapshot | trabalho posterior ao snapshot | cria volume novo a partir do snapshot → troca pelo atual |
| **Recover** | `task update` | idem Update | idem Update | com o estado no volume, recuperar um droplet inalcançável **é** reconstruí-lo |

---

## 5. A metade 2: o agente

### 5.1 Por que hexagonal

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

### 5.2 O laço, passo a passo

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

### 5.3 As ferramentas

| Ferramenta | O que faz | Detalhe que importa |
|---|---|---|
| `shell` | executa comando | usa `bash -c`, **não** `-lc` — ver §9.4 |
| `request_takeover` | pede que uma pessoa assuma | é o que transforma "preciso de ajuda" em estado executável |

---

## 6. Fluxo ponta a ponta

### 6.1 Tarefa que conclui sozinha

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

### 6.2 Tarefa que esbarra numa senha

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

## 7. As cláusulas, uma a uma

Esta é a seção central. Cada entrada tem: o que a documentação exige, por que isso
existe, como foi implementado, e como sabemos que funciona.

---

### C1 — Um agente roda uma tarefa por tela de cada vez

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

### C2 — As telas não são fronteiras de segurança

**Por que existe.** É um aviso, não um recurso. Quem assume que a tela isola vai
gravar credencial de um cliente numa tela achando que a outra não alcança.

**Implementação.** Não há o que implementar — há o que **documentar onde alguém
vai ler**: no `screen-add`, no README e aqui.

```bash
# Cria mais uma tela na MESMA maquina. As telas sao superficies de trabalho
# separadas, NAO fronteiras de seguranca -- quem alcanca uma alcanca o mesmo
# /workspace e as mesmas credenciais de linha de comando.
```

---

### C3 — A visualização mostra o status atual

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

### C4 — O agente pode pedir que você assuma

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

### C5 — Os cinco gatilhos são fechados

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

### C6 — Senha não entra na conversa

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

### C7 — Pausar e avisar, em vez de contornar

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

### C8 — Durável × descartável

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

### C9 — Update, Recover e Reset são diferentes

Já detalhado em §4.4. O ponto conceitual: **Update preserva o estado como ele está
agora; Reset devolve o estado ao snapshot.** Colapsar os dois num só verbo (o erro
da primeira versão) tira do operador a única operação não destrutiva.

---

### C10 — O computador local é separado

**Implementação.** Por construção: nada em `agentd` toca o Mac. A única ponte é o
túnel SSH que **você** abre.

---

## 8. Decisões e por quê

| Decisão | Alternativa | Por que assim |
|---|---|---|
| DigitalOcean | Fly (US$ 5/mês contra US$ 26) | Fly não tem snapshot nativo, que é o `Reset` da documentação |
| droplet 4 GB | 2 GB (US$ 12) | Chrome estoura em 2 GB; OOM no meio do teste polui o resultado |
| nyc3 | outras regiões | 114 ms medidos daqui; acima de ~180 ms o VNC arrasta |
| volume separado | tudo no disco | sem ele, `Update` é impossível |
| `Xvfb` + `x11vnc` + `noVNC` | KasmVNC | KasmVNC daria resolução dinâmica, mas exige `.deb` externo — mais peça para quebrar |
| Chrome | Chromium | o agente navega em site real; compatibilidade importa mais |
| `grok-4.6` | `grok-4.5` | mais recente, com chamada de ferramenta medida |
| trava de arquivo | registro em banco | `flock` é liberado sozinho se o processo morrer |
| estado em arquivo JSON | SQLite | precisa sobreviver ao rebuild sem serviço nenhum subir junto |
| perfil por tela | perfil compartilhado | o Chrome trava o `user-data-dir` — ver §10 |

---

## 9. Armadilhas medidas

### 9.1 O DigitalOcean corrompe user-data não-ASCII

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

### 9.2 `cmd | tee` engole o código de saída

O primeiro `task up` saiu **rc=0 sem ter criado droplet nenhum**. `set: [pipefail]`
resolve; canário confirma rc=201 com e rc=0 sem.

Vale para `| head` também — aconteceu de novo com `go build ... | head -5 && echo OK`.

### 9.3 Espera que não distingue estados

A primeira versão do `02-wait-ready.sh` dizia "aguardando SSH responder" quando o
SSH **já autenticava** e só o arquivo-marca faltava — porque `cat` de arquivo
inexistente devolve rc=1. Custou 12 minutos de diagnóstico na direção errada.

Agora separa três estados e **aborta na hora** se o YAML foi recusado.

### 9.4 `bash -lc` contamina toda saída de comando

Achado por um teste. O shell de **login** carrega o perfil do usuário, e qualquer
mensagem que ele imprima — um `echo` de boas-vindas, um erro de arquivo faltando —
entra na saída de **todo** comando, vai para o histórico do modelo, gasta token e
confunde o agente.

```
esperava marcador de saída vazia, veio
"/Users/andrebassi/.bash_profile: line 19: ... No such file or directory"
```

Corrigido para `bash -c`.

### 9.5 Cortar saída longa pelo fim perde o erro

Numa saída longa, a mensagem que interessa costuma estar na **última** linha.
`truncateOutput` corta pelo meio, preservando começo e fim.

---

## 10. Divergência conhecida: cookies não são compartilhados entre telas

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

## 11. Operação

```bash
# infraestrutura
task check        # binários, token, chave, latência — ANTES de gastar
task up           # volume + droplet + espera + valida
task open         # túnel SSH e a tela no navegador
task screens      # telas ativas, estado durável, recursos
task validate     # as 9 seções
task snapshot     # snapshot do volume durável
task update       # rebuild preservando o durável
task reset        # volta ao snapshot
task destroy      # derruba o droplet (o volume fica, US$ 2/mês)

# dentro da máquina
screen-add 2      # cria a tela 2
screen-remove 2   # derruba (o perfil fica)
agent-status      # telas, estado durável, portas, recursos

# o agente
agentd -screen 1 -prompt "a tarefa"
agentd -resume -task <id> -note "resolvi o login"
```

### Testes

```bash
cd agent && ./scripts/coverage-gate.sh   # mede E reprova
```

| Suíte | O que prova |
|---|---|
| `task validate` | 9 seções da infraestrutura, `erros: 0` |
| `09-persistence-test.sh` | reboot real: serviços sobem, sessão sobrevive |
| `12-update-test.sh` | rebuild real: a fronteira durável×descartável vale |
| `coverage-gate.sh` | 91,7% total, domínio 100% |

⚠️ **Sobre cobertura de branch:** o Go não a mede nativamente, só statements. Em
vez de fingir um número que a ferramenta não produz, a compensação é explícita:
tabelas de teste cobrindo cada ramo de decisão (transições inválidas, os cinco
motivos, entradas malformadas), e domínio em 100%.

---

## 12. Números medidos, 29/08/2026

| | |
|---|---|
| Latência até nyc3 | 114 ms |
| Boot + cloud-init | ~4 min |
| Update completo | ~6 min |
| Reboot até serviços ativos | ~70 s |
| RAM com 1 tela | 980 MB de 3915 (25%) |
| RAM com 2 telas | 1,5 GB (~500 MB por tela) |
| Perfil do navegador, uso leve | 286 MB |
| Chrome | 152.0.7977.64 |
| Custo com droplet ligado | US$ 26,00/mês |
| Custo só do estado | US$ 2,00/mês |
| Cobertura de testes | 91,7% (domínio 100%) |
| Tokens de uma tarefa simples | 723 entrada / 10 saída |
