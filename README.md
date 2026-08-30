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
| [Arquitetura ponta a ponta](#arquitetura-ponta-a-ponta) | o modelo explicado, as 12 cláusulas com código e prova |
| [Auditoria de fidelidade](#auditoria-de-fidelidade-à-documentação) | placar do que existe e do que falta |
| [Avaliação do KasmVNC](#avaliação-do-kasmvnc) | medição, e por que não trocar agora |
| [Avaliação do CloakBrowser](#avaliação-do-cloakbrowser) | por que evasão de anti-bot não entra aqui |
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

Dentro da máquina, `/workspace/agentd`:

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
ssh agent@<host> '/workspace/agentd -catalog install /tmp/do.yaml'

## credencial, pelo stdin — NUNCA em linha de comando, onde `ps` a exporia
ssh agent@<host> 'install -m 600 /dev/stdin /workspace/agent/connectors/secrets/digitalocean-token' \
  <<< "$(pass show bassi/digitalocean/api-token)"
```

Conferindo:

```bash
ssh agent@<host> '/workspace/agentd -catalog list'
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
  /workspace/agentd -screen 1 -task demo-audit \
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

## dentro da máquina
screen-add 2      # cria a tela 2
screen-remove 2   # derruba (o perfil fica)
agent-status      # telas, estado durável, portas, recursos

## o agente
agentd -screen 1 -prompt "a tarefa"
agentd -resume -task <id> -note "resolvi o login"

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
