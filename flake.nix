{
  # Backend de observabilidade do agent-computer, rodando no MAC.
  #
  # Por que no Mac e nao no droplet: a maquina tem 2 vCPU e ~2,9 GB livres, e cada
  # tela custa ~500 MB de Chrome. Gastar essa folga guardando telemetria e cobrar
  # do trabalho para observar o trabalho. Aqui o droplet so EMPURRA (OTLP), o que
  # tambem preserva o invariante testado por 08-validate.sh: nada escuta fora de
  # 127.0.0.1, e o unico ingress da maquina continua sendo a 22.
  #
  # Por que Nix e nao Docker: e a regra do dono para todo recurso local. Aqui ela
  # ainda paga um extra -- os quatro binarios sao nativos aarch64-darwin, sem VM
  # de Linux no meio, entao o backend nao disputa memoria com nada.
  description = "agent-computer -- backend de observabilidade (traces, logs, metricas)";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    process-compose-flake.url = "github:Platonic-Systems/process-compose-flake";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "aarch64-darwin" "x86_64-darwin" "x86_64-linux" "aarch64-linux" ];
      imports = [ inputs.process-compose-flake.flakeModule ];

      perSystem = { pkgs, lib, ... }:
        let
          # Todo estado vive num diretorio so, ignorado pelo git: apagar ./data e
          # o "comecar limpo" completo, sem caçar arquivo em ~/.local.
          dataDir = "./data";
        in
        {
          # `nix run .#observability` sobe os quatro.
          process-compose."observability" = {
            settings.processes = {

              # Metricas. Sobe primeiro porque o metrics_generator do Tempo faz
              # remote_write para ca -- se ela nao estiver de pe, o Tempo loga
              # erro de escrita a cada intervalo e polui o diagnostico.
              victoriametrics = {
                command = pkgs.writeShellApplication {
                  name = "start-victoriametrics";
                  runtimeInputs = [ pkgs.victoriametrics ];
                  text = ''
                    mkdir -p ${dataDir}/victoriametrics
                    exec victoria-metrics \
                      -storageDataPath=${dataDir}/victoriametrics \
                      -httpListenAddr=127.0.0.1:8428 \
                      -retentionPeriod=14d
                  '';
                };
                readiness_probe = {
                  http_get = { host = "127.0.0.1"; port = 8428; path = "/health"; };
                  initial_delay_seconds = 1;
                  period_seconds = 2;
                };
              };

              # Logs. VictoriaLogs em vez de Loki por memoria: ~1,3 GB em regime
              # contra 6-7 GB do Loki em benchmark publicado, e este backend roda
              # num laptop que faz outras coisas.
              victorialogs = {
                command = pkgs.writeShellApplication {
                  name = "start-victorialogs";
                  runtimeInputs = [ pkgs.victorialogs ];
                  text = ''
                    mkdir -p ${dataDir}/victorialogs
                    exec victoria-logs \
                      -storageDataPath=${dataDir}/victorialogs \
                      -httpListenAddr=127.0.0.1:9428 \
                      -retentionPeriod=14d
                  '';
                };
                readiness_probe = {
                  http_get = { host = "127.0.0.1"; port = 9428; path = "/health"; };
                  initial_delay_seconds = 1;
                  period_seconds = 2;
                };
              };

              # Traces. Recebe OTLP direto do agentd -- sem coletor no meio, que
              # seria um quinto processo para repassar bytes sem transformar nada.
              #
              # VictoriaTraces e nao Tempo, e a escolha foi MEDIDA em 31/08/2026:
              # o Tempo 3.0.3 (o unico no nixpkgs) trocou o `ingester` por um
              # modulo `live-store` com partition-ring, desenhado para ingestao
              # via fila. Num no unico ele falha no boot com
              # "mkdir /var/tempo: permission denied" e exige configurar anel de
              # particao para guardar algumas centenas de spans por hora.
              # Complexidade de cluster para volume de laptop.
              #
              # A troca ainda simplifica o conjunto: os tres armazenamentos
              # passam a ser do mesmo ecossistema, com a mesma forma de flag e o
              # mesmo jeito de operar.
              victoriatraces = {
                command = pkgs.writeShellApplication {
                  name = "start-victoriatraces";
                  runtimeInputs = [ pkgs.victoriatraces ];
                  text = ''
                    mkdir -p ${dataDir}/victoriatraces
                    # TLS desligado de proposito: o unico caminho ate aqui e
                    # loopback, e certificado em loopback protegeria contra quem
                    # ja esta no Mac. O que atravessa rede e o tunel/malha, que
                    # ja cifra.
                    exec victoria-traces \
                      -storageDataPath=${dataDir}/victoriatraces \
                      -httpListenAddr=127.0.0.1:10428 \
                      -otlpGRPCListenAddr=127.0.0.1:4317 \
                      -otlpGRPC.tls=false \
                      -retentionPeriod=14d
                  '';
                };
                readiness_probe = {
                  http_get = { host = "127.0.0.1"; port = 10428; path = "/health"; };
                  initial_delay_seconds = 1;
                  period_seconds = 2;
                };
              };

              # A tela. Sobe por ultimo: com as fontes de dados ja de pe, a
              # primeira abertura ja mostra dado em vez de erro de conexao.
              grafana = {
                command = pkgs.writeShellApplication {
                  name = "start-grafana";
                  runtimeInputs = [ pkgs.grafana ];
                  text = ''
                    dataPath="$PWD/data/grafana"
                    mkdir -p "$dataPath"/{provisioning/datasources,provisioning/dashboards,dashboards,plugins,log}
                    cp observability/grafana-datasources.yaml \
                       "$dataPath/provisioning/datasources/"

                    # O painel tambem e PROVISIONADO em arquivo, e nao montado
                    # na tela. Painel construido a mao vive no SQLite do
                    # Grafana, que ninguem versiona: na primeira vez que alguem
                    # apagar ./data para comecar limpo, ele some -- e a pessoa
                    # refaz de memoria, com uma consulta ligeiramente diferente.
                    cp observability/dashboard-agent-computer.json "$dataPath/dashboards/"
                    cat > "$dataPath/provisioning/dashboards/agent-computer.yaml" <<'DASHBOARDS'
apiVersion: 1
providers:
  - name: agent-computer
    type: file
    allowUiUpdates: true
    options:
      path: ./data/grafana/dashboards
      foldersFromFilesStructure: false
DASHBOARDS

                    # As duas fontes do ecossistema Victoria sao plugins
                    # externos, e instalar plugin e trabalho do `grafana cli`,
                    # nao do servidor. O `|| true` e deliberado: sem rede, o
                    # Grafana precisa subir assim mesmo -- com as fontes
                    # marcadas como desconhecidas, o que e visivel na tela e
                    # muito melhor que nao ter tela nenhuma para diagnosticar.
                    # So o de LOGS e plugin externo. Traces vao pela fonte
                    # Jaeger, que e nativa -- o plugin de traces do
                    # VictoriaMetrics NAO existe no catalogo, e pedi-lo devolvia
                    # "404: Plugin not found" engolido pelo `|| true`, o que
                    # deixava a fonte quebrada sem nenhuma mensagem.
                    logsPlugin=victoriametrics-logs-datasource
                    if [ ! -d "$dataPath/plugins/$logsPlugin" ]; then
                      # O --homepath e obrigatorio tambem no cli, e nao so no
                      # server: sem ele a instalacao morre com "Could not find
                      # config defaults", que nao diz o que falta.
                      #
                      # O `|| true` e deliberado: sem rede, o Grafana precisa
                      # subir assim mesmo. Tela com uma fonte quebrada e muito
                      # melhor que nenhuma tela para diagnosticar.
                      grafana cli --homepath ${pkgs.grafana}/share/grafana \
                        --pluginsDir "$dataPath/plugins" plugins install "$logsPlugin" || true
                    fi

                    # Os caminhos vao por `cfg:`, e NAO por GF_PATHS_*.
                    # Medido em 31/08/2026: as variaveis de ambiente sao
                    # ignoradas quando ha --homepath, e o Grafana tenta gravar
                    # dentro do proprio pacote no /nix/store, que e somente
                    # leitura. O erro sai como "failed to connect to database:
                    # mkdir ...: permission denied", que aponta para banco
                    # quando o problema e caminho.
                    # A autenticacao anonima vai por `cfg:` pelo MESMO motivo
                    # dos caminhos: com --homepath, as GF_* sao ignoradas.
                    # Medido em 31/08/2026 -- com elas em env o Grafana subia,
                    # a tela abria, e toda consulta voltava vazia enquanto o log
                    # registrava `userId=0 ... status=401`. O sintoma aparecia
                    # como "No data" no painel, que aponta para a fonte de dados
                    # e nao para autenticacao.
                    exec grafana server \
                      --homepath ${pkgs.grafana}/share/grafana \
                      cfg:auth.anonymous.enabled=true \
                      cfg:auth.anonymous.org_role=Admin \
                      cfg:auth.basic.enabled=false \
                      cfg:auth.disable_login_form=true \
                      cfg:paths.data="$dataPath" \
                      cfg:paths.logs="$dataPath/log" \
                      cfg:paths.plugins="$dataPath/plugins" \
                      cfg:paths.provisioning="$dataPath/provisioning" \
                      cfg:server.http_addr=127.0.0.1 \
                      cfg:server.http_port=3000
                  '';
                };
                depends_on = {
                  victoriatraces.condition = "process_healthy";
                  victorialogs.condition = "process_healthy";
                };
                readiness_probe = {
                  http_get = { host = "127.0.0.1"; port = 3000; path = "/api/health"; };
                  # 20s de folga: a primeira subida baixa dois plugins.
                  initial_delay_seconds = 5;
                  period_seconds = 3;
                  failure_threshold = 20;
                };
              };
            };
          };

          # Ferramentas para trabalhar nas duas camadas, sem instalar nada global.
          #
          # O clang aqui NAO e detalhe de conforto: o clang da Apple nao tem o
          # backend BPF (`clang -target bpf` falha com "No available targets are
          # compatible with triple bpf"), enquanto o do nixpkgs registra bpf,
          # bpfeb e bpfel. Medido em 31/08/2026. Sem este devShell, o objeto BPF
          # nao compila nesta maquina.
          devShells.default = pkgs.mkShell {
            packages = with pkgs; [
              go
              clang
              llvm
              otel-tui
              process-compose
            ];
            shellHook = ''
              export BPF_CLANG=${pkgs.clang}/bin/clang
              echo "agent-computer -- observabilidade"
              echo "  nix run .#observability   sobe traces, logs e metricas"
              echo "  BPF_CLANG=$BPF_CLANG"
            '';
          };
        };
    };
}
