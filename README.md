# agent-computer

Desktop virtual persistente no DigitalOcean para agente autônomo — reprodução
local do modelo descrito em [docs.x.ai/grok-bot/computer-and-apps](https://docs.x.ai/grok-bot/computer-and-apps):
tela própria, navegador com sessão que sobrevive ao desligamento da máquina do
operador, `/workspace` compartilhado, e handoff humano pela mesma tela quando
aparece senha, 2FA ou CAPTCHA.

O Grok Bot Computer é produto fechado — a doc não expõe API, endpoint, parâmetro
nem preço. Isto aqui é a mesma ideia, montada em infraestrutura própria.

## Estado

**No ar e validado** em 2026-08-29. `task validate` fecha com `erros: 0` nas nove
seções, e `scripts/09-persistence-test.sh` prova a promessa central com um reboot
de verdade: os cinco serviços sobem sozinhos e o histórico do Chrome sobrevive.

Lab — serve para testar o conceito, não para produção.

## O que roda dentro

| Peça | Papel |
|---|---|
| `Xvfb` :1, 1920×1080×24 | a tela do agente |
| `openbox` | gerenciador de janelas mínimo (não há desktop completo, por RAM) |
| `x11vnc` `-localhost` | serve a tela por VNC, **só** em 127.0.0.1 |
| `websockify` + `novnc` | a mesma tela no navegador, **só** em 127.0.0.1:6080 |
| Google Chrome | perfil em `/workspace/.browser`; CDP em 127.0.0.1:9222 |
| Claude Code (npm) | o agente, rodando dentro da própria máquina |
| Tailscale | instalado, **não** autenticado (sem authkey no cofre) |

## Acesso — nada é publicado

A tela **não tem porta aberta na internet**. `ufw` deixa passar só a 22, e VNC,
noVNC e CDP escutam apenas em loopback dentro do droplet. O caminho é um túnel
SSH aberto pelo operador:

```bash
task open      # abre o túnel e a tela no navegador local
```

Consequência a assumir: **quem tem a chave SSH tem o desktop**. Não há segunda
senha. Para um lab de duas pessoas isso é a troca certa — uma senha VNC exposta
seria pior. Se o acesso precisar sair da chave, o passo é autenticar o Tailscale
(`sudo tailscale up` no droplet imprime a URL de login) e compartilhar o nó.

## Comandos

```bash
task check      # binários, token, chave SSH e latência — roda ANTES de gastar
task up         # cria o droplet e espera o cloud-init (~6 min)
task open       # túnel SSH + abre a tela no navegador
task ssh        # shell como usuário agent
task status     # estado do droplet e dos serviços da tela
task restart    # reinicia Xvfb/VNC/noVNC/Chrome
task logs       # log do cloud-init (diagnóstico de boot)
task snapshot   # snapshot — o "Update/Reset" da doc
task restore -- --latest
task cost       # custo corrente
task destroy    # destrói (pede confirmação digitada)
```

## Custo — medido em 2026-08-29

| Item | Valor |
|---|---|
| Droplet `s-2vcpu-4gb`, nyc3 | **US$ 24,00/mês** (US$ 0,03571/h) |
| Cobrança | por segundo, mínimo 60s, desde jan/2026 |
| Snapshot | US$ 0,06/GB/mês — sistema base ocupa ~8 GB ≈ **US$ 0,48/mês** |
| Bandwidth incluso | 4 TB |

Padrão barato de uso: `task snapshot` → `task destroy` quando parar de usar, e
`task restore` quando voltar. Guarda o estado por centavos em vez de US$ 24.

## Decisões, com o motivo

**DigitalOcean e não Fly.** Fly sairia ~US$ 5/mês com scale-to-zero contra US$ 24
fixos aqui, mas **não tem snapshot nativo** — e snapshot é justamente o
`Update`/`Recover`/`Reset` que a doc do Grok descreve. No Fly aquilo viraria tar
em R2, na mão. Decisão do dono: DO.

**4 GB e não 2 GB.** 2 GB (US$ 12) faz o Chrome estourar assim que abrem-se
algumas abas, e OOM no meio de um teste polui o resultado. Swap de 2 GB entra
como cinto de segurança, com `vm.swappiness=10`.

**nyc3.** Latência medida daqui: **114 ms** contra um droplet real da região. DO
não tem região no Brasil; nyc3 é a que já hospeda o `rustdesk-relay` da conta.
Acima de ~180 ms o arrasto do mouse fica intolerável em VNC.

**Chrome, não Chromium.** O agente navega em site real; compatibilidade importa
mais que a diferença de licença.

**Resolução fixa 1920×1080.** `x11vnc` não entrega redimensionamento dinâmico
bem. KasmVNC entregaria, mas exige `.deb` de fora do repositório — mais peça para
quebrar num lab. Fica como possível upgrade.

**Snapshot desliga o droplet antes.** Snapshot a quente pode capturar o disco a
meio de uma escrita e o restore volta com o perfil do Chrome corrompido.

## Armadilhas já pagas

### O DigitalOcean corrompe user-data não-ASCII, e o cloud-init recusa calado

A mais cara: **três droplets descartados** antes de achar.

Um `acessível` sai do disco como `C3 AD` e chega no droplet como `C3 83 C2 AD` —
dupla codificação UTF-8 no caminho API → ConfigDrive. O `C2 80` que o travessão
duplo-codificado gera é caractere de controle C1, e o cloud-init então recusa o
**arquivo inteiro**:

```
Failed loading yaml blob. unacceptable character #x0080 ... position 450
```

Três coisas tornam isso difícil de ver:

1. **A recusa é silenciosa.** O droplet reporta `status: done`, sobe normal, aceita
   SSH — e não instalou nada. Sem usuário `agent`, sem `/workspace`, sem pacote.
2. **O motivo sai no stderr.** `cloud-init status --long 2>/dev/null` esconde
   justamente os `recoverable_errors`. Meu primeiro detector fazia isso e passou
   batido duas vezes.
3. **Não é culpa do cliente.** Reproduzido em `doctl` 1.145.0, em 1.167.0 e na API
   REST direta com `jq` — com o payload provado byte-idêntico ao arquivo na origem.
   A corrupção é do lado do DigitalOcean.

Correções, todas no repositório: o `user-data.yaml` é **ASCII puro** com um aviso
de 18 linhas no topo, e o `01-create.sh` reprova **qualquer** byte não-ASCII antes
de criar o droplet — apontando posição e caractere.

### `ssh_authorized_keys: []` reprova no schema

Lista vazia devolve `[] is too short`. Não impede a execução, mas suja os
`recoverable_errors` e mascara problema de verdade. A chave é copiada de
`/root/.ssh` pelo `runcmd`, já que o DO a injeta em root na criação.

### `cmd | tee` engole o código de saída

O primeiro `task up` saiu **rc=0 sem ter criado droplet nenhum** — o `tee` devolve
o próprio código. `set: [pipefail]` no Taskfile resolve; canário confirma rc=201
com e rc=0 sem.

### O check de latência mentia

`speedtest-nyc3.digitalocean.com` foi desativado e nem resolve em DNS; o `curl`
falhava calado e o gate reportava `0ms`. Agora a medição é `ping` contra um droplet
real da região — sem sonda, o script diz que não mediu em vez de inventar número.

### Outras

- **`lib.sh` não tem `set -e`.** É sourceado: o flag vazaria para o script que o
  chamou e mataria tudo no primeiro `grep` sem resultado, sem mensagem de erro.
- **Marca de conclusão explícita.** `/var/lib/agent-computer-ready` existe porque,
  sem ela, não há como distinguir "cloud-init ainda instalando" de "cloud-init
  quebrou no meio".

## Medido nesta máquina, em 2026-08-29

| | |
|---|---|
| Latência até nyc3 | 114 ms (ping contra droplet real da região) |
| Boot + cloud-init completo | ~4 min |
| RAM em repouso, Chrome aberto | 997 MB de 3915 (25%) |
| Disco após instalação | 7,1 GB de 77 |
| Perfil do Chrome após 2 páginas | 157 MB |
| Chrome | 152.0.7977.64 |
| Reboot até serviços ativos | ~70 s |

## Pendências

- [ ] Tailscale autenticado (precisa de authkey ou de um clique do dono)
- [ ] Medir consumo real de RAM com Chrome e algumas abas abertas
- [ ] Avaliar KasmVNC para resolução dinâmica
- [ ] Decidir como o Claude Code de dentro autentica
