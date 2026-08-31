// Programa eBPF: registra todo `execve` bem-sucedido da máquina.
//
// Responde a pergunta que nenhuma instrumentação em espaço de usuário responde
// de forma confiável: O QUE O MODELO EXECUTOU. O agente pode ser instrumentado,
// mas ele é o próprio adversário do modelo de ameaça deste projeto — e um
// registro que o observado pode desligar não é registro. Este roda no kernel,
// fora do alcance do usuário `agent`.
//
// SEM DEPENDÊNCIA DE HEADERS EXTERNOS, e isso é decisão.
// O caminho normal seria incluir `bpf_helpers.h` do libbpf e um `vmlinux.h`
// gerado do BTF da máquina alvo. Os dois foram evitados:
//   - `vmlinux.h` amarraria o objeto ao kernel de onde foi gerado, e o objeto
//     aqui é compilado no Mac e commitado, para a máquina nunca precisar de
//     clang;
//   - `bpf_helpers.h` traria uma árvore de headers para versionar junto.
// Como o programa é um TRACEPOINT — cuja ABI é estável e cujo payload já vem
// montado —, declarar os quatro helpers usados à mão é suficiente e deixa
// visível exatamente o que ele fala com o kernel.

// SEC marca a seção ELF que o carregador procura. `used` impede o compilador de
// remover símbolos que nada neste arquivo referencia.
#define SEC(name) __attribute__((section(name), used))

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef int __s32;

// Tipos de mapa que este programa usa, do uapi do kernel.
#define BPF_MAP_TYPE_RINGBUF 27

// Helpers do kernel, pelos números do uapi. O número É o contrato — ele não
// muda entre versões, ao contrário de nome de símbolo interno.
static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static __u64 (*bpf_get_current_uid_gid)(void) = (void *)15;
static long (*bpf_get_current_comm)(void *buf, __u32 size) = (void *)16;
static __u64 (*bpf_get_current_cgroup_id)(void) = (void *)80;
static long (*bpf_probe_read_kernel_str)(void *dst, __u32 size, const void *src) = (void *)115;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, __u64 size, __u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, __u64 flags) = (void *)132;

// Tamanhos. Cada um com o motivo, porque número mágico em programa de kernel é
// o que ninguém ousa mexer depois.
//
// TASK_COMM_LEN é 16 no kernel e não é negociável — `bpf_get_current_comm`
// escreve exatamente isso.
#define TASK_COMM_LEN 16
// O caminho do binário. 256 cobre com folga o que se vê nesta máquina
// (`/nix/store/<hash>-<nome>/bin/<nome>` é o pior caso, ~120 bytes) e mantém o
// evento pequeno o suficiente para o ring buffer não virar gargalo.
#define MAX_FILENAME_LEN 256

// exec_event é o que atravessa para o espaço de usuário.
//
// A ORDEM E O ALINHAMENTO DOS CAMPOS SÃO CONTRATO com o decodificador em Go: os
// inteiros de 64 bits vêm primeiro para não haver padding entre eles, e os dois
// vetores de bytes por último. Trocar a ordem aqui sem trocar lá produz números
// plausíveis e errados — a pior classe de defeito, porque não falha.
struct exec_event {
    __u64 timestamp_ns;
    __u64 cgroup_id;
    __u32 pid;
    __u32 tgid;
    __u32 uid;
    __u32 gid;
    char comm[TASK_COMM_LEN];
    char filename[MAX_FILENAME_LEN];
};

// events é o canal para o espaço de usuário.
//
// Ring buffer, e não perf buffer: o ring é compartilhado entre CPUs, então a
// ordem dos eventos é preservada — e ordem importa num registro de auditoria,
// onde "o que veio antes" é metade da informação. 256 KiB é potência de dois de
// páginas, como o kernel exige.
struct {
    int (*type)[BPF_MAP_TYPE_RINGBUF];
    int (*max_entries)[256 * 1024];
} events SEC(".maps");

// exec_args espelha o payload do tracepoint sched:sched_process_exec.
//
// Os offsets vêm do arquivo `format` do PRÓPRIO kernel que vai rodar isto,
// medido em 31/08/2026 (kernel 6.12.93):
//
//     common_type            offset 0   size 2
//     common_flags           offset 2   size 1
//     common_preempt_count   offset 3   size 1
//     common_pid             offset 4   size 4
//     __data_loc filename    offset 8   size 4
//     pid                    offset 12  size 4
//     old_pid                offset 16  size 4
//
// Copiar offset de blog é o modo de falha silencioso desta classe de programa:
// compila, carrega, e emite número errado para sempre.
struct exec_args {
    __u16 common_type;
    __u8 common_flags;
    __u8 common_preempt_count;
    __s32 common_pid;
    // __data_loc não é o texto: é um u32 com o COMPRIMENTO nos 16 bits altos e
    // o OFFSET (relativo ao início desta struct) nos 16 bits baixos. Ler este
    // campo como ponteiro devolveria lixo.
    __u32 filename_loc;
    __s32 pid;
    __s32 old_pid;
};

// handle_exec roda a cada execve bem-sucedido da máquina.
//
// `sched_process_exec` e NÃO `syscalls/sys_enter_execve`, e a diferença
// importa: o segundo dispara na TENTATIVA e traz `argv` como ponteiro para o
// espaço do usuário — memória que o processo observado controla e pode
// reescrever entre a leitura e o exec de verdade. Seria uma janela TOCTOU
// dentro do próprio registro de auditoria. Este dispara com a nova imagem já
// instalada, e o kernel já copiou o nome do arquivo para o buffer do evento.
//
// SEM FILTRO POR uid, também por decisão: nesta máquina o Chrome, o Xvfb e o
// x11vnc rodam como o MESMO usuário `agent` para onde as ferramentas do modelo
// caem, então filtrar por uid não separaria o modelo do navegador. E um exec de
// uid 0 é ou o deploy do operador ou uma escalada — as duas coisas que mais se
// quer ver. O volume de exec é de dezenas por minuto; o custo de não filtrar é
// próximo de zero.
SEC("tracepoint/sched/sched_process_exec")
int handle_exec(struct exec_args *args) {
    struct exec_event *event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    // NULL significa ring buffer cheio, e o evento se perde. É contado no lado
    // do userspace comparando a sequência — perda silenciosa num registro de
    // auditoria é o defeito que este programa existe para não ter.
    if (!event) {
        return 0;
    }

    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u64 uid_gid = bpf_get_current_uid_gid();

    event->timestamp_ns = bpf_ktime_get_ns();
    event->cgroup_id = bpf_get_current_cgroup_id();
    // O tgid está nos 32 bits ALTOS e o pid nos baixos. Invertê-los é o erro
    // clássico deste helper, e produz números que parecem PIDs válidos.
    event->pid = (__u32)pid_tgid;
    event->tgid = (__u32)(pid_tgid >> 32);
    event->uid = (__u32)uid_gid;
    event->gid = (__u32)(uid_gid >> 32);

    bpf_get_current_comm(&event->comm, sizeof(event->comm));

    // O caminho do binário, decodificado do __data_loc: os 16 bits baixos são o
    // offset a partir do início da struct do tracepoint.
    //
    // `comm` NÃO substitui isto: ele tem 16 bytes, então
    // `/usr/local/bin/agentd` chega como `agentd` e `python3 /workspace/x.py`
    // como `python3`. Um registro que guardasse só `comm` e se chamasse "o que
    // rodou" estaria mentindo.
    __u32 filename_offset = args->filename_loc & 0xFFFF;
    bpf_probe_read_kernel_str(&event->filename, sizeof(event->filename),
                              (void *)args + filename_offset);

    bpf_ringbuf_submit(event, 0);
    return 0;
}

// A licença é lida pelo verificador do kernel, não é formalidade: helpers
// marcados como GPL-only são recusados sem ela, e a recusa aparece como erro de
// carregamento sem explicação óbvia.
char LICENSE[] SEC("license") = "GPL";
