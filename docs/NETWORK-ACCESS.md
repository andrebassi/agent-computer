# Por onde se alcança a máquina — e como saber qual caminho está em uso

O computador não publica nada. `ufw` deixa passar só a porta 22, e VNC, noVNC,
CDP e a porta HTTP do `agentd` escutam em `127.0.0.1`. Isso é invariante, testado
pela seção 4 do `08-validate.sh`, e **nenhum modo de rede o afrouxa** — todos
mudam por onde se *chega*, nunca o que a máquina *expõe*.

Este documento responde uma pergunta: **por qual caminho o acesso está indo, e o
que fazer quando ele não está disponível.**

---

## O problema que existia antes

Até 31/08/2026 a resolução de endereço era automática e tinha uma reserva
silenciosa: tentava a malha Tailscale, e caía para o IP público sem dizer nada.

Isso resolve o caso comum e **esconde o raro**. Uma malha caída fica idêntica a
uma malha funcionando — os dois casos entregam um endereço, o comando conecta, e
tudo parece certo. O custo aparece depois: quem quer o endereço estável está
usando o instável sem saber, e um diagnóstico de "por que o túnel caiu de novo?"
vai procurar defeito na máquina em vez de na malha.

A reserva não é errada. **Ela só não pode ser a única opção.**

---

## Os quatro modos

`AGENT_NETWORK` declara a intenção. O padrão preserva o comportamento histórico;
os outros três falham alto em vez de cair.

| Modo | O que faz | Falha quando | Use quando |
|---|---|---|---|
| `auto` | **padrão** — malha; se não, IP público | nunca | uso diário, quando qualquer caminho serve |
| `tailscale` | exige a malha | o nó não está nela | quer o endereço estável e quer **saber** se não tem |
| `ssh` | força o IP público, mesmo com a malha no ar | o droplet não existe | está diagnosticando a própria malha |
| `cloudflared` | usa `CLOUDFLARE_TUNNEL_HOSTNAME` | o hostname não foi definido | alguém precisa da tela **sem** entrar no seu tailnet |

O modo `ssh` merece uma linha a mais: forçar o IP público **com a malha
funcionando** parece inútil até o dia em que a malha é a suspeita. Sem ele, não
há como provar que um defeito é dela — toda tentativa passaria pelo caminho que
se quer testar.

### Como o `agent_host()` decide

```
                        AGENT_NETWORK
        ┌─────────────┬───────┴───────┬──────────────┐
      auto          ssh          tailscale      cloudflared
        │             │               │              │
   mesh_address()     │          mesh_address()   $CF_TUNNEL_
        │             │               │            HOSTNAME
    ┌───┴───┐         │           ┌───┴───┐          │
  achou   vazio       │         achou   vazio     ┌──┴──┐
    │       ↓         ↓           │       ↓     def.   vazio
    ↓  droplet_ip  droplet_ip     ↓    🛑 FALHA   ↓    🛑 FALHA
  100.x   IP púb.   IP púb.     100.x         hostname
```

A diferença inteira está nos dois `🛑`. Em `auto` não existe caminho que falhe —
que é a conveniência e o defeito, na mesma propriedade.

### Por que `mesh_address()` é uma função separada

Ela responde *"a malha alcança o nó?"*; `agent_host()` responde *"por onde eu
vou?"*. Fundidas, como estavam, o modo exigente não teria como distinguir **não
está na malha** de **caiu para o IP público** — que é exatamente a confusão que
`AGENT_NETWORK` existe para desfazer.

---

## Saber o que está em uso

```bash
task route
```

```
IP público (165.22.35.97, modo auto)
```

**O modo aparece de propósito.** O endereço sozinho não distingue *escolhi a
malha* de *caí nela*: em `auto` os dois imprimem o mesmo `100.x`, e é a diferença
que interessa quando algo falha.

---

## Ponta a ponta: pôr a máquina na malha

### 1. Criar a authkey

Em <https://login.tailscale.com/admin/settings/keys>, com **as quatro** opções:

| Opção | Por quê |
|---|---|
| **Reusable** | o droplet é recriado a cada `task up`; chave de uso único obrigaria uma nova por rebuild |
| **Ephemeral** | o nó some sozinho no `destroy`. Sem isso o tailnet acumula fantasmas disputando o hostname `agent-computer`, e o `mesh_address()` passa a achar o nó errado |
| **Preauthorized** | senão cada rebuild para esperando aprovação no console — e o `task up` fica pendurado |
| **Tag** (ex. `tag:agent-computer`) | a ACL do tailnet passa a governar o que esse nó alcança. Sem tag ele herda as permissões do seu usuário, que é bem mais do que ele precisa |

Guardar no cofre:

```bash
pass insert bassi/tailscale/authkey
```

### 2. Provisionar

```bash
AGENT_NETWORK=tailscale task network
```

O script busca a chave no `pass`, envia por SSH de root, roda o `up` e apaga a
chave. Depois confirma que o estado remoto é `Running` — **provisionar sem
conferir não é provisionar**, e o `up` pode sair com sucesso e o nó não subir.

### 3. Verificar

```bash
AGENT_NETWORK=tailscale task route
```

```
malha Tailscale (100.70.182.102, modo tailscale)
```

Se aparecer o `🛑`, o nó não está na malha — e nesse modo isso é dito, não
contornado.

---

## 🛑 A credencial não passa pelo cloud-init

Essa é a decisão de segurança do desenho, e vale entender por quê.

O `user-data` do DigitalOcean é servido pelo metadata da nuvem, em
`169.254.169.254`. O `docs/SECURITY.md:129` já registra esse endereço como
**alcançável a partir da ferramenta de shell** — ou seja, pelo modelo, que é o
adversário declarado deste projeto.

Uma authkey ali seria legível justamente por quem ela deveria conter. E o que o
modelo faria com ela não é pequeno: **adicionar nós ao tailnet pessoal do dono**.
Isso é escalada para *fora* da máquina — pior que qualquer coisa que ele consiga
fazer dentro dela.

Por isso o caminho é outro:

```
   Mac                                    droplet
   ───                                    ───────
   pass bassi/tailscale/authkey
        │  SSH de root, pela ENTRADA
        │  do processo — nunca em argv
        └──────────────────────────────► /run/tailscale-authkey
                                            │  tailscale up
                                            ↓  shred -u
                                         ✓ na malha
```

Dois detalhes que parecem excesso e não são:

- **pela entrada do processo, não como argumento.** Argumento aparece em `ps`, e
  o usuário `agent` — logo o modelo — lê `ps`. É o mesmo cuidado que
  `cmd/agentd/main.go` já toma com o token do modelo.
- **`shred -u` logo depois.** A chave existe em disco por segundos, em `/run`,
  que é `tmpfs` e não sobrevive a reboot de qualquer forma. Apagar é a diferença
  entre uma janela de segundos e uma janela até o próximo reboot.

---

## O outro caso: `cloudflared`

A malha resolve o acesso **seu**. Ela não resolve o caso de outra pessoa precisar
ver a tela sem entrar no seu tailnet — convidar alguém para a malha é dar a ele
um nó permanente na sua rede pessoal.

O túnel Cloudflare cobre isso, com dois preços declarados:

1. **o hostname é público** — quem descobrir o nome alcança a porta;
2. **o tráfego passa pela Cloudflare**.

Por (1), a política de **Access na frente não é opcional**. Sem ela o noVNC fica
aberto, e o noVNC é o controle da máquina.

```bash
# o túnel aponta para o noVNC da tela 1
CLOUDFLARE_TUNNEL_HOSTNAME=agent.exemplo.com AGENT_NETWORK=cloudflared task network
```

O token do túnel vem de `bassi/cloudflare/tunnel-token`, pelo mesmo caminho da
authkey — SSH de root, entrada do processo, apagado depois.

⚠️ **Não existe padrão para `CLOUDFLARE_TUNNEL_HOSTNAME`, e é deliberado.** Um
hostname adivinhado resolveria para a máquina de outra pessoa, e a conexão
tentaria autenticar lá. Vazio falha alto, que é o comportamento certo para um
endereço que ninguém pode inferir.

---

## Diagnóstico

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| `task route` diz "o droplet não existe" mas ele existe | faltou `load_token` — o `doctl` roda sem credencial e `droplet_ip` volta vazio | é defeito de script; já aconteceu duas vezes neste repo, e o `task lint` vigia |
| `AGENT_NETWORK=tailscale` falha e o nó existe | o Tailscale **do Mac** está parado | ver a armadilha do WARP, abaixo |
| o nó aparece offline mesmo com o droplet no ar | `tailscaled` roda, mas o nó está `Logged out` | `task network` com a authkey |
| dois nós `agent-computer` na malha | a authkey não era **ephemeral** | remover o fantasma no console e recriar a chave |
| conectou, mas a resolução de nomes quebrou na máquina | o Tailscale reescreveu o resolvedor | o `up` usa `--accept-dns=false` por isso; conferir se alguém o removeu |

### ⚠️ A armadilha do WARP

**Tailscale e Cloudflare WARP são os dois WireGuard, e disputam rota e DNS no
macOS.** Com o WARP no ar, o Tailscale do Mac fica `Stopped` — e sem o Mac na
malha, `AGENT_NETWORK=tailscale` não tem para onde apontar, por mais que o
droplet esteja perfeitamente conectado do lado dele.

Medido em 31/08/2026: WARP no PID 747, Tailscale local em `Stopped`, nó
`agent-computer` presente na malha (`100.70.182.102`) e inalcançável.

O sintoma engana porque o erro fala do **nó remoto**, e a causa está na máquina
local. Antes de mexer no droplet: `tailscale status` aqui.

---

## O que este documento **não** cobre

- **O transporte da telemetria.** Os dois assuntos se tocam — a telemetria vai da
  máquina para o Mac — mas as portas do backend, o túnel reverso e os dois
  endpoints OTLP estão em [`OBSERVABILITY.md`](OBSERVABILITY.md).
- **O modelo de privilégio.** Quem alcança o quê *dentro* da máquina está em
  [`SECURITY.md`](SECURITY.md). Aqui só se trata de como chegar até ela.
- **A ACL do tailnet.** A tag é recomendada acima, mas o que ela permite se
  configura no console do Tailscale, fora deste repositório.
