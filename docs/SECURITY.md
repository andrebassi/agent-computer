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

Sete achados. Os dois primeiros tornavam todo o resto decorativo.

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
lê, não escreve. Quem instala é o operador, **por SSH de root** (ver achado 6:
uma concessão de sudoers aqui valeria para o modelo também).

### 6. Toda regra dada ao operador é dada ao modelo junto

Achado que só apareceu ao verificar na máquina, e é o mais sutil dos seis.

**Operador e modelo são o mesmo usuário `agent`.** O operador entra por SSH como
`agent`; as ferramentas do modelo são rebaixadas para `agent`. Logo, qualquer
concessão de sudoers escrita "para o operador" vale para o modelo.

Aconteceu na mesma sessão: para o operador gerenciar o catálogo, escrevi

```
agent ALL=(agentd) NOPASSWD: /usr/local/bin/agentd -catalog *
```

Parece inofensivo — e desfazia o achado 5, porque o modelo passava a poder
cadastrar conector como `agentd`.

**Corrigido**: a concessão saiu. A autoridade do operador passou a ser a **chave
SSH de root**, que existe só no Mac e que o modelo não alcança por caminho
nenhum. Catálogo, cofre e deploy entram por ali (`root_ssh` e `agentd_run` em
`scripts/lib.sh`).

Regra que fica: **enquanto operador e modelo compartilharem usuário, sudoers não
é lugar para conveniência de operação.**

### 7. Segredo em claro no volume durável

`/workspace/agent/xai.env` era texto puro. O volume é fotografado por `task
snapshot`, e a foto vai para a conta do DigitalOcean: a chave da xAI ficava
legível para quem tivesse o token da conta.

**Corrigido**: cofre gopass cifrado com age. Store no volume durável,
**identidade age no disco do sistema** (`/etc/agentd/gopass`). A separação é o
mecanismo — identidade junto do store tornaria a foto autossuficiente.

---

## Fechado em 30/08/2026: conector não alcança a rede interna

Estava aberto e agora não está. `validateBaseURL` recusava o IP literal —
link-local (`169.254.0.0/16` inteira), loopback, faixa privada, CGNAT e esquema
fora de http/https — mas um **nome** que resolve para lá passava.

A correção não foi resolver o nome no cadastro, e o motivo importa: três
caminhos escapam de qualquer checagem feita antes da conexão.

| Caminho | Por que a validação de cadastro não pega |
|---|---|
| rebinding de DNS | o cadastro viu um IP público; a chamada vai para outro |
| redirect 302 | a URL validada não é a que o servidor mandou seguir |
| registros múltiplos | o resolvedor devolve o interno na segunda consulta |

Fechado no **discador** (`connectors/dialer.go`), que vê o IP final no instante
de abrir o socket — sem janela entre a checagem e o uso. Um endereço interno na
lista resolvida reprova a conexão inteira, em vez de a função pular para o
próximo: um nome que devolve um público e um interno é exatamente a forma do
ataque.

Duas decisões que valem registro:

- **transporte próprio, não `http.DefaultTransport`** — mexer no padrão
  afetaria toda chamada HTTP do processo, inclusive a do modelo; a intenção é
  restringir só o que sai em nome do conector;
- **discar em todos os endereços resolvidos**, e não no primeiro. Parar no
  primeiro parece equivalente e quebra `localhost` (resolve para `::1` antes de
  `127.0.0.1`) — custou sete testes de conector antes de aparecer.

O limite continua o mesmo, e é honesto dizê-lo: **a ferramenta de shell alcança
a rede interna diretamente**, e nada aqui a limita. O que se fechou é o caminho
que o `agentd` percorre EM NOME do modelo, dentro do processo que tem o cofre
aberto — onde a credencial do conector seria anexada à requisição.

Verificado na máquina por `task ssrf-test`, com prova de falha nos dois
sentidos: com o discador desarmado o teste reprovou mostrando o vazamento
(`HTTP 200 {"status":"ok"}` — a porta de tarefas interna, alcançada pelo nome);
restaurado, `erros: 0`.

---

## O que continua aberto

Registrado, não escondido.

| | Estado |
|---|---|
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
task privilege-test    # 11 seções adversariais, rodando como o usuário do modelo
task serve-test        # 10 seções da porta HTTP, incluindo reconciliação após kill -9
```

Resultado medido em 30/08/2026, no droplet `159.203.76.114`: **`erros: 0`** nos
dois.

O teste **tenta a escalada de verdade** e exige que ela falhe. Uma seção que
passe em silêncio é uma proteção ausente — por isso a seção 10 confere o
contrário (a operação continua funcionando) e a 11 confere que a 10 não está
apenas aprovando tudo. Endurecer quebrando o operador não é endurecer, é trocar
de problema.

### O verificador também erra — três vezes nesta sessão

Nenhum dos três era defeito do produto, e todos passariam por defeito dele:

| O que o teste dizia | O que era |
|---|---|
| "`vault.pass` não existe" | existia; o `test -s` rodava como `agent`, que não lê `/etc/agentd`. **Usuário restrito não distingue "não existe" de "não posso ver"** |
| "a operação QUEBROU: `systemctl is-active`" | a permissão estava intacta; `is-active` devolve rc≠0 quando a unidade está **parada**. Recusa se reconhece pela MENSAGEM do sudo, não pelo rc |
| "a tarefa sumiu do disco" | estava lá; o JSON é gravado indentado (`"State": "running"`) e o padrão do `grep` não previa o espaço |

Ele não pode ser substituído por leitura de código: a separação depende de dono
de arquivo, modo, sudoers e **ordem de diretivas do systemd**, e cada um deles
falha em silêncio — a máquina sobe, tudo funciona, e a proteção não está lá.
