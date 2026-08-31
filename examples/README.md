# Exemplos de conectores e habilidades

Este arquivo cataloga **o que já existe**. Para **criar um novo** — o contrato
campo a campo, o passo a passo e as armadilhas — veja
[`docs/EXTENDING.md`](../docs/EXTENDING.md).

## Conectores

Manifestos prontos, todos testados contra a API real. Instalar é copiar o
arquivo para `/workspace/agent/connectors/installed/` e gravar a credencial em
`/workspace/agent/connectors/secrets/`, com permissão `0600`.

| Arquivo | Operações | Credencial | Autenticação |
|---|---|---|---|
| `digitalocean.yaml` | droplets, volumes, snapshots, conta | `bassi/digitalocean/api-token` | bearer |
| `cloudflare.yaml` | zonas, DNS, túneis, verificação de token | `bassi/cloudflare/token` | bearer |
| `gitlab.yaml` | issues | `bassi/gitlab/token` | header `PRIVATE-TOKEN` |
| `github.json` | issues | PAT do GitHub | bearer |

O `digitalocean.yaml` é o mais recursivo do catálogo: com ele o agente enxerga
e opera a própria infraestrutura em que roda.

### O que NÃO dá para conectar, e por quê

Vale saber antes de tentar, para não perder tempo:

| Serviço | Motivo |
|---|---|
| **GLPI da Tinnova** | a API REST está **desativada** (`apirest.php` devolve `["ERROR","API desativada"]`), e há decisão de só usá-la quando for religada. O fluxo de chamado é pelo navegador, de propósito |
| **AWS** | usa SigV4 — assinatura calculada por requisição, não cabeçalho estático |
| **Gmail, Google Calendar** | OAuth com renovação de token; o conector não faz o fluxo |
| qualquer API com mTLS | o conector não apresenta certificado de cliente |

### Limitações do desenho atual

- **sem paginação automática** — a operação devolve a primeira página, e a API
  raramente avisa que há mais. Os manifestos expõem `per_page` para compensar
- **sem upload de arquivo**
- **resposta cortada em 8 KB** — listagem grande volta truncada

## Habilidades

Procedimentos reutilizáveis, invocados com `/nome`. Instalar é copiar para
`/workspace/agent/skills/`.

| Arquivo | Para quê |
|---|---|
| `web-diagnosis.md` | site fora do ar — de fora para dentro, separando DNS de rede de aplicação |
| `change-review.md` | conferir o próprio trabalho antes de publicar |

O limite é 8 KB por habilidade: o conteúdo entra no prompt **a cada iteração**
da tarefa, não uma vez.

### O que faz uma habilidade boa

As duas de exemplo seguem o mesmo formato, e ele não é decorativo:

- **passos numerados com o comando exato**, não descrição do que fazer
- **o porquê de cada passo**, para o agente saber quando o passo não se aplica
- **as armadilhas do procedimento** — em `web-diagnosis.md`, não reiniciar
  antes de coletar o log, porque o reinício apaga a prova
- **uma seção de regras no fim**, com o que vale para o procedimento inteiro

Habilidade que só lista comandos não acrescenta nada ao que o modelo já sabe. O
valor está no julgamento embutido: a ordem dos passos e o que não fazer.
