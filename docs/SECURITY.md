# Segurança do agent-computer

Revisão de 30/08/2026, feita junto com a entrada do cofre gopass. Documento de
referência para quem for mexer no modelo de privilégio — e, principalmente, para
quem for "endurecer" alguma coisa que está frouxa **de propósito**.

Referência de método: `~/works/dux/duxbot-local/plans/sec-plan.md`. Três padrões
de lá foram aplicados aqui, e um achado inteiro (a escalada por arquivo de
ambiente) foi encontrado por procurar exatamente a classe que ele descreve.

---

## O adversário

Não é alguém na internet — a máquina só escuta em `127.0.0.1` e o acesso é por
túnel SSH. O adversário é **o próprio modelo**.

Ele tem, por desenho, uma ferramenta de shell que roda comando arbitrário. Isso
não é um defeito a corrigir: é a função do produto. A pergunta de segurança é
outra — **o que ele alcança a partir dali**, e o que deve continuar fora do
alcance mesmo quando ele age contra o interesse do dono (por injeção de prompt
numa página que leu, por falha de alinhamento, ou por defeito).

Três coisas devem ficar fora do alcance:

| | Por quê |
|---|---|
| credenciais que não são dele | a chave da xAI paga a conta; o token da porta abre a máquina |
| as próprias regras | habilidade é a instrução dele; conector é o alcance de rede dele |
| o binário do serviço | quem escreve o binário é dono do serviço |

---

## O que a revisão encontrou

Seis achados. Os dois primeiros tornavam todo o resto decorativo.

### 1. O modelo era root

`agent` tinha `sudo: ['ALL=(ALL) NOPASSWD:ALL']` no cloud-init, e a ferramenta
de shell rodava como `agent`. Um `sudo cat` lia qualquer arquivo da máquina.

Nenhum controle de segredo sobrevive a isso — inclusive o cofre que estava sendo
construído na mesma sessão, que teria nascido decorativo.

**Corrigido**: usuário `agentd` próprio para o serviço, ferramentas do modelo
rebaixadas para `agent`, e o `NOPASSWD:ALL` trocado por lista fechada.

Ficaram **fora** da lista, cada um porque é root por outro caminho:

| Comando | Como vira root |
|---|---|
| `apt-get` | script de mantenedor roda como root |
| `chown`, `chmod` | muda o dono do próprio arquivo de senha do cofre |
| `systemctl edit`/`link`/`set-property` | injeta `ExecStart` arbitrário |
| `journalctl` sem unidade fixa | lê log de qualquer serviço |
| `dd`, `tee`, `cp`, `cat`, `bash`, `su` | leitura ou escrita arbitrária |

`pkill` ficou preso ao padrão `agentd*`: aberto, um `pkill sshd` derruba o
acesso à máquina.

### 2. O modelo podia trocar o binário do serviço

O `agentd` morava em `/workspace/agentd`, dono `agent` — tratado como estado
durável para sobreviver ao `update`. Só que `/workspace` é justamente onde o
modelo escreve.

Ele substituiria o arquivo, e o serviço — que roda como `agentd`, dono do cofre
— executaria o conteúdo dele no próximo restart. **Quem escreve o binário do
serviço é dono do serviço.**

Agravante: a regra de sudoers escrita para o operador gerenciar o catálogo
(`agent ALL=(agentd) NOPASSWD: /workspace/agentd ...`) transformava isso numa
escalada imediata, sem nem esperar restart.

**Corrigido**: o binário foi para `/usr/local/bin/agentd`, `root:root 0755`, e o
deploy passou a instalá-lo **por SSH de root**, cuja chave existe só no Mac.

> Enviar como `agent` e mover com `sudo` **não** resolveria: o conteúdo ainda
> viria de um arquivo que o modelo controla. A escrita precisa acontecer por um
> caminho que ele não alcança.

Preço aceito: `update` reconstrói o disco do sistema e leva o binário junto —
todo update pede `task deploy` depois. O script avisa.

### 3. O modelo desligava o próprio rebaixamento

Classe idêntica à escalada `booster` → `root` pelo `config.env` do duxbot.

A unidade tinha `Environment=AGENTD_TOOL_USER=agent` **antes** de
`EnvironmentFile=-/workspace/agent/xai.env`. O systemd aplica as diretivas na
ordem em que aparecem, e **para a mesma variável o último vence**. O arquivo
estava em `/workspace` — onde o modelo escreve.

Uma linha `AGENTD_TOOL_USER=off` no arquivo, um restart, e as ferramentas dele
voltavam a rodar como `agentd`: dono do cofre.

**Corrigido**, nas duas pontas:

- ordem invertida — o arquivo primeiro, a linha fixa depois;
- o arquivo saiu de `/workspace` para `/etc/agentd/xai.env`, `0640 agentd:agentd`.

> O mecanismo que faz isso funcionar é o mesmo que o duxbot documenta: **o
> systemd lê o `EnvironmentFile` como PID 1, antes de baixar para `User=`** —
> então o serviço sobe normalmente com um arquivo que o próprio usuário do
> serviço não escreve.

### 4. Injeção de comando na unidade de avisos

`ExecStart=/bin/sh -c '... ${AGENT_WEBHOOK:+-webhook "$AGENT_WEBHOOK"}'`, com
`AGENT_WEBHOOK` vindo de `/workspace/agent/notify.env` — outro arquivo do
modelo. Um valor que fechasse a aspa emendava comando arbitrário.

**Corrigido**: o shell saiu da unidade. `ExecStart=/usr/local/bin/agentd
-notify-drain`, e o binário lê a variável ele mesmo. Sem shell não há o que
injetar.

### 5. O modelo reescrevia as próprias regras

`skills/` e `connectors/` ficavam sob `/workspace/agent/`, com escrita do
modelo. Ele podia editar a habilidade que o instrui e cadastrar um conector
apontando para onde quisesse — inclusive `169.254.169.254`, o metadata da nuvem.

**Corrigido**: os dois diretórios passaram a ser `agentd:agentd 0755` — o modelo
lê, não escreve. Quem instala é o operador, por `sudo -u agentd`.

### 6. Segredo em claro no volume durável

`/workspace/agent/xai.env` era texto puro. O volume é fotografado por `task
snapshot`, e a foto vai para a conta do DigitalOcean: a chave da xAI ficava
legível para quem tivesse o token da conta.

**Corrigido**: cofre gopass cifrado com age. Store no volume durável,
**identidade age no disco do sistema** (`/etc/agentd/gopass`). A separação é o
mecanismo — identidade junto do store tornaria a foto autossuficiente.

---

## O que continua aberto

Registrado, não escondido.

| | Estado |
|---|---|
| **`baseURL` de conector sem validação** | um manifesto pode apontar para `169.254.169.254`. Mitigado: só o operador instala conector agora. Não corrigido: falta recusar link-local e faixa privada |
| **Conversa não expira** | a saída de toda ferramenta fica no volume para sempre, e entra em cada foto. Se o modelo leu um segredo em algum momento, ele está gravado ali. Sem expurgo nem redação |
| **`NoNewPrivileges` desligado no `agentd-api`** | e **de propósito**: o rebaixamento usa `sudo`, que é setuid. Ligar quebraria justamente o mecanismo que tira o cofre do alcance do modelo |
| **Root na máquina lê tudo** | cofre é cifra em repouso mais separação de usuário, não isolamento contra root. Quem tem a chave SSH de root do Mac contorna tudo — e é assim que o deploy funciona |

### Por que não trocar `sudo` por capacidade

Foi considerado `AmbientCapabilities=CAP_SETUID` + `SysProcAttr.Credential`, no
espírito do `CAP_NET_BIND_SERVICE` do duxbot — daria para ligar
`NoNewPrivileges=yes`.

**Não serve**: capacidade *ambient* é herdada pelo filho. O `bash` do modelo
herdaria `CAP_SETUID` e voltaria a ser `agentd`. Piora em vez de melhorar.

`sudo` fica, com a concessão declarada em uma linha auditável de sudoers.

---

## Como verificar

```bash
task privilege-test    # 10 seções adversariais, rodando como o usuário do modelo
```

O teste **tenta a escalada de verdade** e exige que ela falhe. Uma seção que
passe em silêncio é uma proteção ausente — por isso a seção 10 confere o
contrário: que a operação continua funcionando. Endurecer quebrando o operador
não é endurecer, é trocar de problema.

Ele não pode ser substituído por leitura de código: a separação depende de dono
de arquivo, modo, sudoers e **ordem de diretivas do systemd**, e cada um deles
falha em silêncio — a máquina sobe, tudo funciona, e a proteção não está lá.
