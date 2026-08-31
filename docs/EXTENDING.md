# Estender o agente: conector, habilidade e runner

São **três** pontos de extensão, e escolher o errado custa retrabalho. A pergunta
que decide está na primeira seção.

Para o que cada arquivo é e onde mora, veja [`STATE-FILES.md`](STATE-FILES.md).

## Qual dos três você quer

| Você quer que o agente… | Use | Por quê |
|---|---|---|
| **chame uma API** com credencial que ele não pode ver | **conector** | o `agentd` monta a requisição; a credencial nunca entra no prompt nem em subprocesso |
| **siga um procedimento** que você repete | **habilidade** | é texto que entra no prompt; não executa nada por si |
| **use outro agente de código** (codex, droid…) | **runner** | é um binário externo que a delegação invoca |

Erro comum: escrever uma habilidade que explica como chamar uma API com `curl`.
Funciona, e é pior — a credencial passa a viver na linha de comando do shell, ao
alcance do modelo, e some do controle do `agentd`.

---

## Conector: uma API vira ferramenta

### O contrato

Um manifesto YAML ou JSON. YAML é preferível pelo motivo que o próprio exemplo
demonstra: **comentário**. Escopo de token, limite de página e o porquê de cada
operação não cabem em JSON.

```yaml
name: gitlab                    # [a-zA-Z0-9_-]{1,48} — vira parte do nome da ferramenta
description: >-                 # o modelo lê isto para decidir SE usa
  Trabalha com issues do GitLab pela API, em vez de clicar pelo site.

base_url: https://gitlab.com/api/v4

auth:
  type: header                  # bearer | header | query
  header_name: PRIVATE-TOKEN    # só para type: header
  secret_ref: gitlab-token      # nome da entrada em connectors/secrets/

operations:
  - name: list_issues           # o modelo verá "gitlab.list_issues"
    description: >-
      Lista issues de um projeto. O id é numérico, ou o caminho com %2F.
    method: GET
    path: /projects/{id}/issues # {id} é preenchido pelo parâmetro de mesmo nome
    schema:                     # JSON Schema; é o que o modelo recebe
      type: object
      properties:
        id: {type: string}
        state: {type: string, enum: [opened, closed, all]}
      required: [id]

  - name: create_issue
    method: POST
    path: /projects/{id}/issues
    body_params: [title, description]   # estes vão no CORPO
    schema: {...}
```

**Os três tipos de autenticação**, e nada além deles:

| `type` | Como vai | Quando |
|---|---|---|
| `bearer` | `Authorization: Bearer <segredo>` | o mais comum |
| `header` | `<header_name>: <segredo>` | GitLab, e APIs com cabeçalho próprio |
| `query` | `?<param>=<segredo>` | último recurso — valor em URL entra em log de servidor |

**`body_params` importa.** Sem ele, todo parâmetro vai para a query string — e
título de issue numa URL acaba gravado no log de acesso do servidor alheio.

### Como criar, passo a passo

```bash
# 1. copie o mais parecido e edite
cp examples/connectors/gitlab.yaml /tmp/meu-servico.yaml

# 2. instale
agentd -catalog install /tmp/meu-servico.yaml

# 3. grave a credencial — pelo stdin, sem eco, nunca em argumento
agentd -catalog secret meu-servico-token

# 4. confira que virou ferramenta
agentd -catalog list

# 5. prove contra a API real, ANTES de dar ao modelo
agentd -connector-probe https://api.meu-servico.com/health
```

### Como usar

Anexe com `@`. Só o conector citado vira ferramenta:

```bash
agentd -prompt "@meu-servico liste os itens abertos"
```

⚠️ **Anexar é obrigatório de propósito.** O catálogo inteiro custaria token a
cada iteração e daria ao modelo alcance a serviços que a tarefa não pediu.

### O que dá errado, e como reconhecer

| Sintoma | Causa |
|---|---|
| a ferramenta não aparece para o modelo | esqueceu o `@`, ou o nome não bate — confira com `-catalog list` |
| 403 numa operação e 200 em outra | escopo do token. É confuso porque o conector *parece* meio quebrado |
| filtro "não funciona" numa listagem | a API limitou `per_page` em silêncio; declare o máximo real no schema |
| resposta cortada | teto de 8 KB por resultado de ferramenta. Restrinja pela query, não pelo corte |
| `base_url aponta para a rede interna` | é o discador: nem IP interno nem nome que resolva para ele (ver [`SECURITY.md`](SECURITY.md)) |

### O que NÃO dá para conectar

| Serviço | Motivo |
|---|---|
| AWS | SigV4 — assinatura calculada por requisição, não cabeçalho estático |
| Gmail, Google Calendar | OAuth com renovação de token |
| qualquer API com mTLS | o conector não apresenta certificado de cliente |

Também não há paginação automática nem upload de arquivo.

---

## Habilidade: um procedimento que se repete

### O contrato

Um arquivo Markdown. Nome `[a-zA-Z0-9_-]{1,48}`, **máximo 8 KB** — e o teto
importa: o conteúdo entra no prompt **a cada iteração** da tarefa, não uma vez.

### Como criar

```bash
# 1. escreva o procedimento
vi /tmp/deploy-check.md

# 2. instale pelo catálogo, que valida nome e tamanho
agentd -catalog skill-save deploy-check < /tmp/deploy-check.md

# 3. confira
agentd -catalog list
```

### Como usar

```bash
agentd -prompt "/deploy-check confira o serviço de pagamentos"
```

O marcador some do texto; o conteúdo entra **depois** do pedido, delimitado, para
o objetivo vir primeiro.

⚠️ **Caminho de arquivo não vira habilidade**: `/workspace/projects` continua
sendo um caminho. A distinção está em `domain/connector.go` e existe porque a
primeira versão anexava uma habilidade chamada "workspace" *e* comia o caminho
do texto.

⚠️ **Habilidade inexistente é silenciosa pela porta HTTP** — o aviso só sai no
CLI. Se o comportamento não mudou, confira o nome antes de suspeitar do modelo.

### O que faz uma habilidade boa

As de exemplo seguem um formato, e ele não é decorativo:

- **passos numerados com o comando exato**, não a descrição do que fazer;
- **o porquê de cada passo**, para o agente saber quando ele *não* se aplica;
- **as armadilhas** — em `web-diagnosis.md`, não reiniciar antes de coletar o
  log, porque o reinício apaga a prova;
- **uma seção de regras no fim**, com o que vale para o procedimento inteiro.

Habilidade que só lista comandos não acrescenta nada ao que o modelo já sabe. O
valor está no julgamento embutido: a ordem dos passos e o que **não** fazer.

O exemplo mais útil de ler é `examples/skills/web-search.md` — ele existe porque
o IP de datacenter é bloqueado pelos buscadores, então a ordem das fontes é a
diferença entre achar e não achar.

---

## Runner: outro agente de código

### O contrato

Uma entrada em `/workspace/agent/runners.json`. Comando como **vetor**, nunca
string:

```json
{
  "codex": {
    "cmd": ["codex", "exec", "--yolo", "--skip-git-repo-check", "-"],
    "stdin": true,
    "description": "OpenAI Codex"
  },
  "droid": {
    "cmd": ["droid", "exec", "--skip-permissions-unsafe", "-f", "{prompt}"],
    "description": "Factory Droid"
  }
}
```

| Campo | O que faz |
|---|---|
| `cmd` | vetor que vai direto para `exec.Command`, sem shell |
| `{prompt}` | trocado pelo caminho de um arquivo temporário `0600` com a tarefa |
| `stdin` | `true` manda o texto pela entrada padrão, para CLIs que a esperam |
| `description` | aparece na mensagem quando o runner não existe |

### Como cadastrar

O arquivo é `agentd:agent 0640` — o modelo lê e não escreve. Editar é como root:

```bash
ssh root@<maquina> "vi /workspace/agent/runners.json"
# e instalar o binário, senão o runner falha dizendo qual falta
```

### Como usar

```
delegate_to_code {"task": "...", "runner": "codex"}
```

Sem `runner`, é o Claude Code — e nada do que já estava testado muda.

### As três recusas, e por quê

| Recusa | Motivo |
|---|---|
| **binário que é interpretador** (`sh`, `bash`, `env`, `xargs`) | com shell no meio voltam `;`, `&&` e `$(...)`, e com eles `sudo` — que desfaz o rebaixamento do modelo para o usuário `agent` |
| **nome fora do catálogo** | o modelo escolhe entre nomes, nunca monta comando. A mensagem lista os que existem |
| **binário ausente na máquina** | falha **nomeando** o que falta; a mensagem é a documentação de instalação |

⚠️ O prompt viaja em **arquivo**, não em argumento: argumento é visível em `ps`
para qualquer usuário da máquina, inclusive o do modelo. E tarefa de código é
longa — por argumento estouraria o limite do sistema.

---

## Depois de criar qualquer um dos três

```bash
task guardrails-test   # dono e modo dos arquivos, e que o modelo não escreve
task suites            # nada do que já funcionava quebrou
```

E a regra que vale para os três: **teste contra o alvo real antes de entregar ao
modelo.** Conector que devolve 403, habilidade com comando errado e runner não
instalado falham de formas que parecem defeito do agente — e o diagnóstico vai
para o lugar errado.
