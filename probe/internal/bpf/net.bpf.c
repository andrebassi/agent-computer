// Programa eBPF: registra toda conexao TCP que SAI da maquina.
//
// Fecha o limite que docs/SECURITY.md admite em :202-205 -- "a ferramenta de
// shell alcanca a rede interna diretamente, e nada aqui a limita". Ele nao
// limita: REGISTRA. Mas registrar no kernel e a diferenca entre saber e
// supor, e e o que permite ver uma varredura de rede interna acontecendo.
//
// Sem dependencia de headers externos, pelo mesmo motivo do exec.bpf.c: o
// objeto e compilado no Mac e commitado, e a maquina nunca precisa de clang.

#define SEC(name) __attribute__((section(name), used))

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef int __s32;

#define BPF_MAP_TYPE_RINGBUF 27

static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static __u64 (*bpf_get_current_uid_gid)(void) = (void *)15;
static long (*bpf_get_current_comm)(void *buf, __u32 size) = (void *)16;
static __u64 (*bpf_get_current_cgroup_id)(void) = (void *)80;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, __u64 size, __u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, __u64 flags) = (void *)132;

#define TASK_COMM_LEN 16

// Estados TCP, do uapi. So dois interessam aqui.
#define TCP_SYN_SENT 2
#define TCP_CLOSE 7

// net_event e o que atravessa para o espaco de usuario.
//
// A ORDEM DOS CAMPOS E CONTRATO com o decodificador em Go: os de 64 bits
// primeiro, depois os de 32, depois os de 16, depois os vetores. Trocar a
// ordem aqui sem trocar la produz enderecos e portas plausiveis e errados.
struct net_event {
    __u64 timestamp_ns;
    __u64 cgroup_id;
    __u32 pid;
    __u32 tgid;
    __u32 uid;
    __u32 daddr_v4;
    __u32 saddr_v4;
    __u16 dport;
    __u16 sport;
    __u16 family;
    __u16 padding;
    char comm[TASK_COMM_LEN];
    __u8 daddr_v6[16];
};

// events e o canal para o espaco de usuario, separado do de exec.
//
// Dois ring buffers e nao um: os dois programas tem volumes muito diferentes, e
// um buffer compartilhado faria uma rajada de conexoes descartar eventos de
// exec -- perdendo o registro mais importante por causa do mais frequente.
struct {
    int (*type)[BPF_MAP_TYPE_RINGBUF];
    int (*max_entries)[256 * 1024];
} net_events SEC(".maps");

// net_args espelha o payload de sock:inet_sock_set_state.
//
// Offsets lidos do `format` do PROPRIO kernel que roda isto, em 31/08/2026
// (6.12.93):
//
//     skaddr      offset 8    size 8
//     oldstate    offset 16   size 4
//     newstate    offset 20   size 4
//     sport       offset 24   size 2
//     dport       offset 26   size 2
//     family      offset 28   size 2
//     protocol    offset 30   size 2
//     saddr[4]    offset 32   size 4
//     daddr[4]    offset 36   size 4
//     saddr_v6    offset 40   size 16
//     daddr_v6    offset 56   size 16
struct net_args {
    __u16 common_type;
    __u8 common_flags;
    __u8 common_preempt_count;
    __s32 common_pid;
    const void *skaddr;
    __s32 oldstate;
    __s32 newstate;
    __u16 sport;
    __u16 dport;
    __u16 family;
    __u16 protocol;
    __u8 saddr[4];
    __u8 daddr[4];
    __u8 saddr_v6[16];
    __u8 daddr_v6[16];
};

// handle_connect roda a cada mudanca de estado de socket TCP.
//
// FILTRA a transicao CLOSE -> SYN_SENT, e o filtro e de CORRECAO, nao de
// volume. Essa transicao acontece dentro de `tcp_connect()`, em contexto de
// PROCESSO do chamador -- so ali `bpf_get_current_pid_tgid()` devolve quem
// discou. As outras transicoes rodam em soft-IRQ, onde o PID atual e lixo, e
// usa-las atribuiria conexoes ao processo errado. Atribuicao errada num
// registro de auditoria e pior que ausencia: ela acusa quem nao fez.
//
// `sock/inet_sock_set_state` e nao `kprobe/tcp_v4_connect`: o kprobe recebe um
// `struct sock *` e exigiria percorrer `sk->__sk_common.skc_daddr` com CO-RE e
// BTF, alem de depender de um nome de simbolo que ja mudou entre versoes. Este
// tracepoint carrega os enderecos no PROPRIO payload, IPv4 e IPv6, origem e
// destino. E o caso mais limpo de "tracepoint estavel ganha do kprobe".
SEC("tracepoint/sock/inet_sock_set_state")
int handle_connect(struct net_args *args) {
    if (args->oldstate != TCP_CLOSE || args->newstate != TCP_SYN_SENT) {
        return 0;
    }

    struct net_event *event = bpf_ringbuf_reserve(&net_events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    __u64 pid_tgid = bpf_get_current_pid_tgid();

    event->timestamp_ns = bpf_ktime_get_ns();
    event->cgroup_id = bpf_get_current_cgroup_id();
    event->pid = (__u32)pid_tgid;
    event->tgid = (__u32)(pid_tgid >> 32);
    event->uid = (__u32)bpf_get_current_uid_gid();
    event->family = args->family;
    event->padding = 0;

    // As portas vem em ORDEM DE REDE no payload e sao convertidas no espaco de
    // usuario, nao aqui: fazer a conversao nos dois lados e a forma classica de
    // uma delas ser esquecida, e o sintoma e a porta 443 aparecer como 47873 --
    // numero plausivel que ninguem questiona.
    event->sport = args->sport;
    event->dport = args->dport;

    // Os quatro bytes do IPv4 viram um u32 na ordem em que estao. A
    // interpretacao fica no decodificador, junto com a do IPv6.
    __builtin_memcpy(&event->daddr_v4, args->daddr, 4);
    __builtin_memcpy(&event->saddr_v4, args->saddr, 4);
    __builtin_memcpy(&event->daddr_v6, args->daddr_v6, 16);

    bpf_get_current_comm(&event->comm, sizeof(event->comm));
    bpf_ringbuf_submit(event, 0);
    return 0;
}

char LICENSE[] SEC("license") = "GPL";
