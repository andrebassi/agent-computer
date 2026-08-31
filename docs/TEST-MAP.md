# Mapa de cobertura — funcionalidade × teste

Cada funcionalidade construída neste projeto, e **qual teste a exercita**. Sem
este mapa, "está tudo testado" é afirmação; com ele, é verificável — dá para
apontar a linha que cobre cada item, ou admitir que não existe.

Rodar tudo:

```bash
task lint         # gate dos scripts, no Mac — não toca na máquina
task nixos:validate   # avalia a config NixOS, no Mac
task suites       # as 4 suítes de máquina
task functional   # os 3 que chamam o modelo de verdade
task hostile      # entrada hostil, degradação e concorrência
```

## A regra que este mapa aplica

Uma funcionalidade tem cobertura quando existe um teste que **falha se ela for
removida**. Contar que algo "existe" não é cobertura — foi assim que
`claude --version` passou por prova de que a delegação funciona, quando prova
apenas que o binário executa.

## Camada 1 — em processo (`task test:cov`, no Mac)

92 arquivos de teste, 90,8% de cobertura total, domínio 100%, com `-race`.

⚠️ **É cobertura de _statement_, não de branch** — o Go não mede branch
nativamente, e inventar o número seria pior que não ter. A compensação é
declarada: os testes usam tabela cobrindo cada ramo de decisão (transição
inválida, motivo de bloqueio, entrada malformada), e o domínio fica em 100%. Cobrem o laço, o
domínio e os adapters com dublê. **Não** cobrem fiação, permissão nem caminho.

## Camada 2 — máquina (`task suites`)

| Funcionalidade | Teste | Seção |
|---|---|---|
| units da tela sobem (Xvfb, openbox, x11vnc, noVNC, Chrome) | `08-validate` | 2 |
| volume durável montado e separado | `08-validate` | 2b, 2c |
| portas só em loopback | `08-validate` | 3 |
| firewall ativo e sem expor tela | `08-validate` | 4 |
| X responde em 1920×1080 | `08-validate` | 5 |
| Chrome vivo, CDP responde, perfil certo | `08-validate` | 6 |
| noVNC serve a página | `08-validate` | 7 |
| **a tela desenha pixel de verdade** | `08-validate` | 8 |
| **as unidades essenciais sobem no BOOT** | `08-validate` | 11 |
| agente de código instalado e executável | `08-validate` | 10 |
| dois usuários distintos | `27-privilege-test` | 1 |
| serviço roda como `agentd` | `27-privilege-test` | 2 |
| modelo não lê senha nem identidade do cofre | `27-privilege-test` | 3, 4 |
| modelo não vira root por sudo | `27-privilege-test` | 5 |
| modelo não escreve o binário do serviço | `27-privilege-test` | 6 |
| modelo não reescreve as próprias regras | `27-privilege-test` | 7 |
| modelo não desliga o próprio rebaixamento | `27-privilege-test` | 8 |
| unidade de avisos sem shell | `27-privilege-test` | 9 |
| **o operador ainda consegue operar** | `27-privilege-test` | 10 |
| **a lista fechada reprova de verdade** | `27-privilege-test` | 11 |
| porta HTTP: saúde, loopback, 401 | `25-serve-test` | 1–4 |
| criar tarefa, conflito 409 com dica | `25-serve-test` | 5, 6 |
| consulta devolve estado e resposta | `25-serve-test` | 7 |
| **reconciliação depois de `kill -9`** | `25-serve-test` | 8 |
| **aviso enfileirado sobrevive à sessão morta** | `25-serve-test` | 9 |
| timer de avisos armado | `25-serve-test` | 10 |
| cofre é a origem dos segredos do serviço | `32-end-to-end` | 2 |
| **delegação roda rebaixada** | `32-end-to-end` | 4 |
| catálogo chega ao modelo | `32-end-to-end` | 6 |
| estado sobrevive a restart | `32-end-to-end` | 8 |
| **contrato igual nos dois caminhos de deploy** | `32-end-to-end` | 12 |
| **conector não alcança a rede interna, nem por nome** | `35-connector-ssrf-test` | 2, 3 |
| **detector de laço bloqueia a tarefa de verdade** | `36-guardrails-test` | 8b |
| **teto de custo em dólar morde** | `36-guardrails-test` | 8c |
| **teto global de tarefas simultâneas** | `internal/adapters/driving/api` | 6 casos, com 429 pela porta HTTP |
| **campo desconhecido recusado nas ferramentas** | `driven/tools` | 5 casos |
| **parâmetro não declarado recusado no conector** | `driven/connectors` | 8 casos, com teste de FIAÇÃO |
| o custo por turno é medido, com cache separado | `36-guardrails-test` | 8c |
| **a lição gravada chega ao prompt da tarefa seguinte** | `36-guardrails-test` | 6 |
| o modelo não escreve os arquivos de memória | `36-guardrails-test` | 2 |
| catálogo de runners sem shell, com os 5 cadastrados | `36-guardrails-test` | 3 |
| runner fora do catálogo é recusado com a lista | `36-guardrails-test` | 4 |
| **tarefa normal não dispara detector nenhum** | `36-guardrails-test` | 8 |
| turnos acumulados persistidos na tarefa | `36-guardrails-test` | 10 |
| o metadata da nuvem existe (senão o teste não prova nada) | `35-connector-ssrf-test` | 1 |
| **conector legítimo continua alcançando fora** | `35-connector-ssrf-test` | 4 |

## Camada 3 — funcional (`task functional`, chama o modelo)

| Funcionalidade | Teste |
|---|---|
| **o modelo executa tarefa comum** | `13-integration-test` §3 |
| **o modelo usa conector (chamada de API real)** | `13-integration-test` §4 |
| **habilidade entra no prompt** | `13-integration-test` §5 |
| take-over: bloqueia, avisa na tela, retoma | `13-integration-test` §6–8 |
| abandono libera a tela | `13-integration-test` §9 |
| estado durável entre invocações | `13-integration-test` §10 |
| **delegação: Grok navega, Claude Code escreve, `unittest` valida** | `17-delegation-test` |
| **navegador navega e lê página de verdade** | `17-delegation-test` §2 |
| **busca na web: fonte certa, não só valor certo** | `21-web-search-test` |

## Camada 4 — hostil (`task hostile`)

A camada que faltava, e que achou o `panic`. A regra dela: **erro é resposta
aceitável, `panic` não é** — e cada ataque confere que o serviço continua de pé.

| Ataque | Seção |
|---|---|
| 12 corpos malformados (JSON truncado, tipo errado, campo faltando) | A |
| corpo de 200 KB contra o teto de 64 KB | B |
| 5 formas de autenticação errada | C |
| rota e método inexistentes, travessia no id | D |
| **degradação: o cofre some** | E |
| **6 chamadas concorrentes na mesma tela** | F |
| estado íntegro depois de tudo | G |

## Camada 5 — gates no Mac (não tocam a máquina)

| Gate | O que pega |
|---|---|
| `task lint` | variável órfã, uso da API sem `load_token`, suíte sem trava, **mensagem procurada que o produto não emite** |
| `task nixos:validate` | config NixOS: sintaxe, ASCII estrito, avaliação do sistema inteiro |
| `task test:cov` | cobertura ≥90% de statements + domínio 100% + detector de corrida |

## A trava: por que rodar duas suítes ao mesmo tempo não é possível

Toda suíte toma `scripts/suite-lock.sh` antes de começar. Não é zelo — é
correção de um defeito medido em 30/08/2026: uma execução órfã continuou viva,
uma segunda subiu por cima, e as duas mexeram no mesmo `agentd`. O estrago:

| Efeito | Por que engana |
|---|---|
| log entrelaçado, com **dois** `erros:` no mesmo arquivo | quem lê o fim vê um número que não é o da sua execução |
| `Failed to connect to port 8787` no meio | a outra instância reiniciou o serviço |
| reconciliação após `kill -9` reprovou | por contaminação, não por defeito — manda caçar bug que não existe |

Os dois sentidos quebram: o vermelho mente e o verde também. A trava fica no
**Mac**, não na máquina — travar lá resolveria tarde, com as duas já mexendo no
estado. `task lint` reprova suíte que esqueça de tomá-la.

## O que o `21` passou a exigir, e por quê

Reescrito em 30/08/2026. A versão anterior era teste-teatro: sem `fail`, sem
contagem, sem olhar a resposta — passava com a habilidade desinstalada e com a
resposta vazia. Agora cada pergunta exige três coisas, e cada uma pega o que as
outras deixam passar:

| Exigência | Pega |
|---|---|
| a tarefa concluiu | travamento e erro de execução |
| a resposta tem número | resposta evasiva ("não consegui obter") |
| **a fonte esperada foi usada** | o valor certo pelo caminho errado |

A terceira é a que justifica o teste existir. Preço de bitcoin obtido por
buscador em vez do atalho CoinGecko está **certo hoje e quebra amanhã** — o
buscador bloqueia o IP de datacenter. Conferir só o número aprovaria a rota que
vai falhar.

Mesma correção no `17`: ele provava que o `unittest` do agente passa, o que
`format_count` satisfaz com um número inventado em `main.py`. Agora o valor é
comparado com `api.github.com` lido **daqui**, com margem de 50 estrelas para o
tempo entre as duas leituras — e sem aprovar por omissão quando a API não
responde.

## Ativo agora não é o mesmo que volta depois

O achado mais caro desta rodada, e o que mais se parecia com outra coisa. O
`agentd-api` — a unidade central, que atende toda a API de tarefas — perdeu o
`wantedBy` ao migrar para NixOS. Consequência:

| | |
|---|---|
| depois do reboot | a porta HTTP **não sobe** |
| `systemctl status` do sistema | `running` |
| unidades em falha | **nenhuma** |
| `systemctl is-enabled agentd-api` | `linked`, não `enabled` |

`after` e `requires` só ordenam; **nada começa sem alguém que queira**. No
Ubuntu isso vinha de um `systemctl enable` no cloud-init — a migração trouxe a
unidade e deixou o `enable` para trás.

O diagnóstico foi para o lugar errado duas vezes antes de chegar aqui: a suíte
HTTP reprovou com **nove erros** (porta sem escutar, health vazio, `000` no
lugar de `401`, criação de tarefa falhando), todos descendentes de uma causa; e
o `SIGKILL` no log parecia OOM, quando era o próprio teste de reconciliação.

Fechado pela seção 11 do `08-validate`, que confere `is-enabled` das unidades
essenciais e `multi-user.target.wants` das telas — provada reprovando o defeito
antes do rebuild (`agentd-api NAO sobe no boot (linked)`) e aprovando depois.

## Telas 2..9: por que o marcador, e não `systemctl enable`

`screen-add` gravava `enable --now`. No NixOS `/etc/systemd/system` é
**read-only** (aponta para o store), e o comando falha:

```
Failed to enable unit: File /etc/systemd/system/xvfb@2.service: Read-only file system
```

Nenhuma tela além da 1 subia — e o log ficava com cara de contradição, porque a
tarefa seguinte, que não precisava de navegador, concluía normalmente.

Agora `screen-add` grava um marcador em `/workspace/agent/screens/<N>` e dá
`systemctl start`; o `agent-screens.service` lê os marcadores no boot. Três
ganhos sobre o `enable`:

| | |
|---|---|
| funciona nos dois sistemas | um mecanismo só, sem ramificar por SO |
| a tela sobrevive à **troca de SO** | o marcador está no volume, não no disco do droplet |
| `screen-add` confere o efeito | `systemctl start` devolve 0 antes de a unidade ficar de pé |

Provado: `screen-add 2` → 5 unidades ativas → `reboot` → voltaram sozinhas.

⚠️ O marcador **não** podia vir de `systemd.tmpfiles.rules`: as regras de
`/workspace/agent` são recusadas com `unsafe path transition` (o dono muda de
`agent` para `agentd` no meio do caminho) e descartadas em silêncio, sem falhar
a unidade. Quem cria e ajusta esses diretórios é o oneshot
`agent-state-ownership`.

## Guardrails do laço

Camada nova, documentada em [`GUARDRAILS.md`](GUARDRAILS.md). O que ela fechou:

| Contenção | Antes |
|---|---|
| detecção de laço de ferramenta | inexistente — `ToolResult.Failed` era escrito por toda ferramenta e **lido por ninguém** |
| teto de turnos por TAREFA | o contador zerava a cada `Resume`; retomada era ilimitada |
| resposta truncada | `finish_reason: "length"` virava `done` **com sucesso** |
| `Resume` com tela ocupada | tarefa `blocked` virava `failed`, perdendo trabalho e pedido de ajuda |
| observabilidade do laço | `service.Agent` não tinha logger nenhum |
| memória entre tarefas | inexistente |

A regra que ela segue, e que o ralph não segue: **detectar é código, conter é
mudança de estado, e o serviço LÊ o que escreveu.** No ralph o prompt recebe o
caminho do arquivo de lições e um pedido para o modelo lê-lo — nenhuma linha de
código lê o conteúdo, embora a documentação afirme o contrário.

## O que NÃO tem cobertura, e é honesto dizer

| Item | Por quê |
|---|---|
| `request_secret` (pedido de senha na tela) | precisa de terminal interativo; há teste em processo, não na máquina |
| `browser_fill` e `browser_click` isolados | exercitados de through pela delegação, não por teste próprio |
| retenção de conversa | sem expurgo — **decisão aceita**, não pendência (o porquê está em `SECURITY.md`) |


## Histórico: o que cada camada achou

Vale registrar, porque justifica a existência de cada uma:

| Camada | Achado |
|---|---|
| máquina | sudoers descartado inteiro; `locks/` com dono errado pondo toda tela em 409; `agentd-notify` como usuário errado quebrando a proatividade em silêncio |
| funcional | **`panic` por ponteiro nulo** derrubando o binário; trava 0644 que `flock` não abre; três scripts de teste quebrados, um deles **passando** com a verificação vazia |
| NixOS | **`agentd-api` não subia no boot** — a unidade central perdeu o `wantedBy` na migração e ficou 26 min fora do ar depois de um reboot, sem nenhuma unidade em falha |
| NixOS | **`screen-add` quebrado**: `systemctl enable` num `/etc/systemd/system` read-only — nenhuma tela além da 1 subia |
| NixOS | **as regras `tmpfiles` de `/workspace/agent` nunca aplicaram** — `unsafe path transition`, descartadas em silêncio |
| sweep de idioma | `"ativa"` → `"activeTask"` **dentro da string** que o teste procura — reprovava uma trava intacta |
| API de terceiro | `droplet_ip` consultado a cada comando: um soluço do DigitalOcean reprovou 3 seções com a máquina de pé |
| a lista deste mapa | **`21-web-search-test` não verificava nada** — disparava 4 perguntas, imprimia "concluída" e saía `rc=0` sempre, com a instalação da habilidade falhando em `Permission denied` ao lado |
| própria infra de teste | **duas suítes concorrentes** contra a mesma máquina — log entrelaçado, `erros: 1` mentiroso numa e `erros: 0` sem valor na outra |
| hostil | campo desconhecido aceito com 201 — um `"screens"` em vez de `"screen"` ia para a tela 1 sem nada indicar o engano |
