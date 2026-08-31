# O que o kernel vê do que o modelo faz — e como sei que ele está vendo

Este documento responde uma pergunta que nenhuma outra camada deste projeto
responde: **o que o modelo de fato executou nesta máquina?**

---

## Por que isto existe

O modelo de ameaça declarado em [`SECURITY.md`](SECURITY.md) é **o próprio
modelo**. A ferramenta de shell é irrestrita por desenho — é a função do
produto, não um defeito —, e o documento admite o limite honesto (`:202-205`):

> a ferramenta de shell alcança a rede interna diretamente, e nada aqui a limita

Disso decorre uma consequência que costuma passar batida: **toda instrumentação
em espaço de usuário é instrumentação que o observado pode contornar.** O
`agentd` instrumentado registra o que ele *pediu* para acontecer. Um comando que
o modelo dispare por um caminho que o agente não conhece não aparece em lugar
nenhum.

eBPF é a única camada que observa de fora do alcance dele: roda no kernel, e o
usuário `agent` não a desliga.

⚠️ Com uma exceção honesta, na seção final.

---

## O que ele registra hoje

Duas probes, e a segunda fecha um limite que a documentação de segurança admite.

### Conexões de saída — `sock/inet_sock_set_state`

```
15:29:46 curl -> 140.82.112.6:443       [publico]
15:29:46 curl -> 169.254.169.254:80     [PRIVADO]
15:29:47 Chrome_ChildIOT -> 142.251.167.188:5228  [publico]
```

`SECURITY.md:202-205` admite: *"a ferramenta de shell alcança a rede interna
diretamente, e nada aqui a limita"*. Esta probe **não limita — registra**. E a
marca `PRIVADO` é o que separa "chamou a API do modelo" de "varreu a sub-rede"
sem alguém ter de reconhecer faixas de endereço de cabeça. `169.254.169.254` é o
endereço de metadados da nuvem: quem o alcança lê as credenciais da instância.

Três decisões, cada uma com o motivo:

- **Filtra `CLOSE -> SYN_SENT`, e o filtro é de CORREÇÃO, não de volume.** Essa
  transição acontece dentro de `tcp_connect()`, em contexto de processo do
  chamador — só ali `bpf_get_current_pid_tgid()` devolve quem discou. As outras
  rodam em soft-IRQ, onde o PID atual é lixo, e usá-las atribuiria conexões ao
  processo errado. **Atribuição errada num registro de auditoria é pior que
  ausência: ela acusa quem não fez.**
- **Tracepoint e não `kprobe/tcp_v4_connect`**: o kprobe receberia um
  `struct sock *` e exigiria percorrer `sk->__sk_common.skc_daddr` com CO-RE e
  BTF, além de depender de um nome de símbolo que já mudou. Este tracepoint
  carrega os endereços no próprio payload, v4 e v6.
- **Dois ring buffers, não um**: os volumes são muito diferentes, e um buffer
  compartilhado faria uma rajada de conexões descartar eventos de `exec` —
  perdendo o registro mais importante por causa do mais frequente.

### 🛑 O defeito que o teste unitário NÃO pegou

A primeira versão convertia a ordem de bytes da porta **duas vezes**, e a 443
saía como 47873 (0x01BB → 0xBB01); a 80, como 20480.

O teste unitário passava. Ele montava o registro com a **mesma suposição errada**
do decodificador, e concordava com o defeito — um teste escrito pela mesma
cabeça que escreveu o código herda a suposição dela.

Quem pegou foi a execução na máquina real, olhando um número que não podia estar
certo. A causa: o tracepoint já faz `ntohs()` antes de preencher o campo, e o
indício estava no próprio arquivo `format` do kernel — o `print fmt` usa `%hu`.

> Contrato de ABI se confere no `format` do kernel, não na memória. E a asserção
> de um teste de rede tem de ser sobre o **valor exato** da porta: "houve
> conexão" passaria com o defeito.

### Todo `execve` — `sched/sched_process_exec`

Um programa atachado ao tracepoint `sched:sched_process_exec`, que dispara a
cada `execve` bem-sucedido da máquina:

```
2026-08-31T14:44:02Z uid=0 pid=7123 cgroup=48000 comm=kernel-to-graf /tmp/kernel-to-grafana-13799
```

| Campo | O que é |
|---|---|
| `timestamp_ns` | relógio monotônico do kernel, convertido para hora do mundo pelo coletor |
| `cgroup_id` | inode do cgroup v2 — é o que distingue o que o `agentd` disparou do que o Chrome disparou |
| `pid` / `tgid` | thread e processo |
| `uid` / `gid` | quem executou |
| `comm` | nome curto, **truncado em 16 bytes pelo kernel** |
| `filename` | o caminho completo do binário |

Repare na linha de exemplo: `comm=kernel-to-graf` está cortado, enquanto
`filename` traz `/tmp/kernel-to-grafana-13799` inteiro. **É por isso que `comm`
não substitui `filename`**: `/usr/local/bin/agentd` chegaria como `agentd`, e
`python3 /workspace/x.py` como `python3`. Um registro que guardasse só `comm` e
se chamasse "o que rodou" estaria mentindo.

---

## As decisões, e o que cada uma custou

### `sched_process_exec`, e não `syscalls/sys_enter_execve`

O segundo parece o óbvio — é literalmente o syscall. Mas:

- ele dispara na **tentativa**, não no fato: um `execve` que falha com `ENOENT`
  emite o mesmo evento que um bem-sucedido;
- o `argv` dele é `const char *const __user *` — ponteiro para vetor de
  ponteiros no espaço do usuário. Ler exige laço limitado, e **o conteúdo pode
  mudar entre a leitura e o exec de verdade**, porque a memória é do processo
  observado. Seria uma janela TOCTOU dentro do próprio registro de auditoria.

`sched_process_exec` dispara com a nova imagem já instalada, e o `filename` vem
por `__data_loc` — o kernel já copiou a string para o buffer do evento. Nada a
ler do espaço do usuário, nada que o modelo consiga reescrever depois.

**O preço, declarado**: perde-se o `argv`. Recuperá-lo de `/proc/<pid>/cmdline`
é *best effort* e não foi implementado — processo de vida curta já saiu, e um
`argv` recuperado assim seria heurística apresentada como fato.

### Tracepoint, e não kprobe

Nome de símbolo interno não é contrato. O caminho do exec no kernel já se chamou
`do_execve`, `do_execveat_common` e `bprm_execve`. Um coletor preso a esse nome
quebra num upgrade — e, se o atach for tolerante, quebra **em silêncio**, que é
o pior desfecho num registro de auditoria: ele sobe, não falha, e não vê nada.

O tracepoint tem ABI estável, e seu payload já vem montado. A consequência
amarra o resto do desenho: **sem CO-RE, o objeto BPF não tem relocação
dependente da versão do kernel** — então ele é compilado no Mac, vai commitado,
e a máquina nunca precisa de clang.

### Sem filtro por uid — e isto é achado, não descuido

O reflexo é filtrar por `uid == 1000`, o usuário para onde as ferramentas do
modelo caem. Nesta máquina isso **não separa nada**: Chrome, Xvfb, x11vnc,
openbox e noVNC rodam como o **mesmo** usuário `agent`. O filtro incluiria o
processo mais barulhento da máquina.

E um `execve` de uid 0 é ou o deploy do operador ou uma escalada — as duas
coisas que mais se quer ver. Filtrar por uid 1000 cegaria justamente para a que
importa.

O volume real é de dezenas por minuto. O custo de não filtrar é próximo de zero.

### Sem headers do libbpf

O programa em C declara à mão os oito helpers que usa, pelos números do uapi.
Não inclui `bpf_helpers.h` nem `vmlinux.h`:

- `vmlinux.h` amarraria o objeto ao kernel de onde foi gerado — e ele é
  compilado no Mac;
- `bpf_helpers.h` traria uma árvore de headers para versionar junto.

Para um tracepoint, cujo payload já vem montado, declarar os helpers é
suficiente — e deixa visível exatamente o que o programa fala com o kernel.

### Os offsets vieram da máquina, não de um blog

```
common_type            offset 0   size 2
common_flags           offset 2   size 1
common_preempt_count   offset 3   size 1
common_pid             offset 4   size 4
__data_loc filename    offset 8   size 4
pid                    offset 12  size 4
old_pid                offset 16  size 4
```

Lidos de `/sys/kernel/tracing/events/sched/sched_process_exec/format` no próprio
kernel que roda isto (6.12.93, medido em 31/08/2026). Copiar offset de outro
lugar é o modo de falha silencioso desta classe de programa: compila, carrega, e
emite número errado para sempre.

⚠️ `__data_loc` **não é o texto**: é um `u32` com o comprimento nos 16 bits altos
e o offset nos baixos. Lê-lo como ponteiro devolve lixo.

---

## Onde o código mora

Módulo Go **separado**, em `probe/` — não dentro de `agent/`. Três motivos:

1. `cilium/ebpf` não pode virar dependência do binário que **abre o cofre**;
2. o coletor precisa de privilégio, e o `agentd` foi desenhado para **rebaixar**
   — um binário com os dois papéis é o que `SECURITY.md` passou sete achados
   desmontando;
3. o gate de cobertura de `agent/` deixaria de significar algo com um pacote que
   não tem como ser testado no Mac.

```
probe/
  internal/bpf/exec.bpf.c      o programa de kernel
  internal/decode/             bytes -> struct        100% de cobertura
  internal/shipper/            fila e entrega          94%
  internal/sample/             PSI e /proc             92%
  internal/collector/          carrega e atacha        EXCLUÍDO, ver abaixo
  cmd/agent-probe/             o binário
```

---

## Como sei que ele está vendo

O pacote `collector` é **excluído do gate de cobertura**, e o motivo é físico:
carregar programa BPF exige um kernel Linux e privilégio, e o Mac onde ele é
escrito não tem nem um nem outro. Testá-lo ali exigiria simular a chamada
`bpf()`, o que provaria que o simulador funciona.

Excluir e dizer **onde a prova está** é diferente de abrir mão dela. A prova é
`scripts/46-ebpf-test.sh` (`task probe:test`), e ela tem três propriedades, cada
uma vinda de um erro já pago neste repositório:

**1. Gatilho determinístico, nunca pedido ao modelo.**
Um canário copiado com nome aleatório roda quando mandado, sempre. Teste que
depende do modelo cooperar é intermitente por construção — está medido no
`README`: pedindo repetição explícita, o modelo repetiu duas vezes e parou.

**2. Asserção sobre o nome exato**, não sobre "chegou alguma coisa". Um coletor
que emitisse lixo passaria num teste de "veio algum evento". O nome aleatório do
canário não aparece por acaso.

**3. Prova de falha nos dois sentidos.** Com o coletor no ar tem que achar; sem
ele, tem que **não** achar. Só o segundo prova que o teste enxerga alguma coisa —
uma verificação que passa nos dois estados não verifica nada.

Mais duas seções: que o usuário `agent` **não escreve** no binário do coletor
(verificado pela tentativa, não pelo modo do arquivo), e que o registro traz o
caminho absoluto, e não só o `comm` truncado.

A suíte roda **duas vezes seguidas** por convenção do repositório: teste que
mexe em processo deixa estado atrás, e uma execução só prova que ele passa numa
máquina limpa — que é o estado em que ele nunca vai rodar de novo.

### Antes de instalar: o gate de viabilidade

`scripts/43-ebpf-feasibility.sh` (`task ebpf:gate`) pergunta à máquina se ela
suporta o coletor, em vez de descobrir no meio do deploy: versão de kernel, BTF
em `/sys/kernel/btf/vmlinux`, `tracefs` montado, e a presença de **cada
tracepoint** que os programas anexam.

O último item é o que envelhece. Tracepoint é contrato estável, mas contrato
some entre versões maiores, e a falha seria no `Attach` — em tempo de execução,
na máquina, longe de quem editou. O gate a antecipa.

O gate exige **nove** tracepoints, e não os dois que os programas usam hoje:

```
sched/sched_process_exec   sched/sched_process_fork   sched/sched_process_exit
sock/inet_sock_set_state   syscalls/sys_enter_connect syscalls/sys_exit_connect
signal/signal_generate     oom/mark_victim            syscalls/sys_enter_openat
```

Os sete a mais são os das **próximas probes** (seção final). Medir agora custa
uma leitura de diretório e responde antes de alguém escrever o código: se um
kernel futuro deixar de expor `oom/mark_victim`, isso aparece no gate, e não no
meio da implementação. O preço é que o gate reprova uma máquina onde o coletor
atual funcionaria — aceito de propósito, porque a máquina é sempre a mesma e o
falso verde custa mais que o falso vermelho.

A lista é ajustável por ambiente, e isso não é conveniência:

| Variável | Padrão | Para quê |
|---|---|---|
| `EBPF_TRACEPOINTS` | os nove acima | os hooks exigidos |

É o que torna a **prova de falha** possível (`task ebpf:gate:proof`): apontando a
variável para um tracepoint que não existe, o gate tem de reprovar; com a lista
real, tem de aprovar. Sem os dois sentidos, um gate que aprovasse qualquer
entrada pareceria saudável — e um gate que nunca reprovou provavelmente não é
gate.

---

## 🛑 O buraco que nenhuma capacidade fecha

O sudoers concede ao usuário `agent` — **logo ao modelo** — `systemctl stop *`,
sem restrição de unidade.

```bash
sudo systemctl stop agent-probe
```

`Restart=always` não ajuda: `stop` é explícito.

Isto **não se conserta na máquina**. As correções possíveis:

1. **Estreitar o sudoers para unidades nomeadas.** É a mudança certa, e vale
   além disto: hoje `sudo systemctl stop sshd` derruba o acesso à máquina, pelo
   mesmo verbo que o `pkill` já protege (ele está preso ao padrão `agentd*`
   justamente porque *"aberto, um `pkill sshd` derruba o acesso"*). É decisão à
   parte deste trabalho.
2. **Batimento no Mac.** O backend alerta quando o fluxo para. **A ausência de
   dado vira o sinal.** É o único mecanismo que não depende de o adversário
   cooperar, e é aplicável hoje.

> Regra derivada: um coletor que roda na máquina que observa é sempre desligável
> por quem tem privilégio ali. A prova de que ele está vivo mora fora da máquina.

---

## Detalhes de operação que custaram tempo

| O que | O que era |
|---|---|
| O clang da Apple | **não tem backend BPF**: `error: unable to create target: 'No available targets are compatible with triple "bpf"'`. O do nixpkgs registra `bpf`, `bpfeb`, `bpfel` |
| O clang **wrapper** do nixpkgs | passa no `-print-targets` e **falha na compilação**: injeta `-mmacos-version-min`, que o `-Werror` transforma em erro. Por isso a detecção COMPILA um objeto de teste em vez de consultar |
| `find` sem `-L` | no nixpkgs `bin/clang` é **symlink** para `bin/clang-21`; sem seguir links, some justamente o compilador que serve |
| `command -v true` | devolve o **builtin** do shell, não um caminho — o `cp` do canário falhava e o teste reprovava dizendo que a probe não enxergava, enquanto ela enxergava. Falso negativo custa mais caro: manda consertar o que está certo |

---

## O nome da unidade é decisão de segurança

`agent-probe`, nunca `agentd-probe`. Dois motivos, dos sudoers atuais:

1. `pkill -9 -f agentd*` casaria o segundo nome — o modelo poderia matar o
   coletor com uma concessão que já existe;
2. `journalctl -u agentd-api.service` e `-u agentd-notify.service` são as duas
   únicas unidades cujo log o `agent` lê. `agent-probe` fica **fora** dessa
   lista, e isso é o desfecho certo: **o auditado não lê o log da auditoria.**

Pelo mesmo raciocínio, **não** se acrescenta uma regra de `journalctl` para ele:
`SECURITY.md` registra que operador e modelo compartilham o usuário `agent`, e
toda concessão escrita "para o operador" vale para o modelo.

---

## A unidade

Roda como **root**, e a decisão foi medida, não presumida.

`/sys/kernel/tracing` é `700 root:root` nesta máquina, e o usuário `agent`
recebe `Permission denied` ao ler o `id` do tracepoint. Isso é checagem de
**DAC**, não de capacidade: `CAP_PERFMON` não abre arquivo sem permissão.

A alternativa seria um usuário próprio com `CAP_DAC_READ_SEARCH` — que lê
**qualquer** arquivo da máquina, inclusive `/etc/agentd/vault.pass`. Os dois leem
tudo; só um admite. Root com `CapabilityBoundingSet` apertado é o honesto.

```
User = root
CapabilityBoundingSet = CAP_BPF CAP_PERFMON CAP_DAC_READ_SEARCH
NoNewPrivileges = true          # seguro aqui: não usa sudo, não executa mais nada
MemoryMax = 96M                 # o coletor nunca pode causar a degradação que mede
CPUQuota = 15%
ProtectControlGroups = false    # precisa ler /sys/fs/cgroup para traduzir o id
```

Sem shell no `ExecStart` (achado 4), config em `/etc/agent-probe/` e **nada sob
`/workspace`** (achado 3: um `EnvironmentFile` em caminho gravável pelo modelo
foi a escalada que desligou o rebaixamento das ferramentas).

E `wantedBy = multi-user.target` **explícito**: a falta dessa linha no
`agentd-api` deixou o serviço 26 minutos fora do ar depois de um reboot, sem
nenhuma unidade em falha. Num coletor de auditoria o mesmo defeito é pior — ele
não coleta, e nada aponta para isso.

---

## Próximas probes, e o que cada uma responderia

| Hook | Responderia |
|---|---|
| `sched_process_fork` | de quem cada processo descende — é o elo sólido com o trecho da ferramenta que o disparou |
| `sock/inet_sock_set_state` | que conexão TCP saiu de fato |
| `syscalls/sys_enter_connect` | o destino **pedido**, inclusive UDP (logo, DNS), e o que falhou |
| `signal/signal_generate` | quem matou o quê — inclusive quem matou o `agentd` |
| `oom/mark_victim` | por que aquilo morreu |
| `sys_enter_openat` + prefixos | tocou onde não devia |

`sched_switch` fica **fora**: dezenas de milhares de eventos por segundo numa
máquina de 2 vCPU com Chrome. O coletor viraria a causa da degradação que mede.
