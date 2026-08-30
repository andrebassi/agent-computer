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
| `task validate` | 9 seções: units, volume, fronteira, portas, firewall, X, Chrome, noVNC, pixel |
| `task integration-test` | **12 seções na máquina real, contra o Grok real**: do estado durável ao take-over, com conector e habilidade |
| `scripts/09-persistence-test.sh` | reboot real: serviços sobem sozinhos, sessão do navegador sobrevive |
| `scripts/12-update-test.sh` | rebuild real: `/workspace` sobrevive, `/scratch` e pacote manual somem |
| `agent/scripts/coverage-gate.sh` | 91,4% de cobertura, domínio em 100% |

Lab — serve para testar o conceito, não para produção.

## Documentação

| Documento | O que traz |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | **Comece por aqui.** Arquitetura ponta a ponta, o modelo do Grok Bot explicado, as 10 cláusulas uma a uma com código e prova, decisões, armadilhas |
| [`docs/fidelity.md`](docs/fidelity.md) | Auditoria cláusula por cláusula, com placar do que existe e do que falta |

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
task validate     # as 9 seções
task snapshot     # snapshot do volume durável
task update       # rebuild preservando o durável
task reset        # volta ao snapshot, descarta trabalho recente
task destroy      # destrói o droplet (o volume fica)
```

Para criar tela: `task ssh`, depois `screen-add 2`.

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

### Divergência: cookies não são compartilhados entre telas

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
- [ ] **Cookies compartilhados entre telas** — divergência com motivo técnico; os
      quatro contornos avaliados estão documentados, nenhum é limpo
- [x] ~~Tailscale autenticado~~ — na malha como `agent-computer`; os scripts
      preferem a malha, com o IP público como reserva testada
- [ ] **KasmVNC** — daria resolução dinâmica no lugar do 1920×1080 fixo; avaliar exige
      subir o droplet e medir, e o ganho é conforto, não capacidade

### A pendência que não é técnica

O projeto está completo e **sem uso definido**. Reproduz o modelo, tem 35 cláusulas
atendidas e passa em quatro suítes — mas ninguém o usa para nada real, e custa
US$ 2/mês parado.

O passo que destravaria valor é escolher **uma tarefa repetitiva de verdade** e ver
se ele a executa melhor que à mão. Sem isso, permanece um estudo bem-feito.
