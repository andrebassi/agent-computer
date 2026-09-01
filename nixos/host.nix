# Configuracao declarativa do agent-computer em NixOS.
#
# Importado pelo instalador (nixos-infect) via NIXOS_IMPORT, entao o
# `configuration.nix` e o `hardware-configuration.nix` continuam sendo os que ele
# gera -- com a rede do DigitalOcean e a chave SSH de root embutidas. Este
# arquivo NAO os substitui.
#
# O instalador e um script que poe NixOS POR CIMA de um Linux que ja esta
# rodando: o droplet nasce Ubuntu (unica imagem que o DigitalOcean oferece),
# ele instala o Nix ali dentro, constroi o sistema a partir deste arquivo,
# reescreve o boot e reinicia. O sistema de arquivos raiz do Ubuntu e apagado
# no processo -- por isso o estado que importa mora no volume separado.
#
# # Por que existe, se o cloud-init do Ubuntu funciona
#
# O caminho do Ubuntu continua valendo e e o padrao. Este e o SEGUNDO caminho,
# escolhido por AGENT_OS=nixos. Ele existe porque tres defeitos desta sessao
# foram todos da mesma classe -- estado imperativo divergindo da intencao sem
# avisar -- e os tres deixam de ser possiveis aqui:
#
#   sudoers com erro de sintaxe    -> nao compila, em vez de descartar o arquivo
#                                     inteiro e tirar todo o sudo do usuario
#   diretorio com dono errado      -> systemd.tmpfiles.rules e declaracao, nao
#                                     uma sequencia de 10 passos que pode
#                                     rodar fora de ordem
#   unidade com User= errado       -> o usuario e parte da mesma expressao que
#                                     cria a unidade
#
# # O que NAO e declarativo aqui, e por que
#
# O binario `agentd` e compilado no Mac e instalado por scp de root em
# /usr/local/bin. Empacota-lo como derivacao Nix e trabalho a parte. O caminho
# e mantido igual ao do Ubuntu de proposito: mudar rippearia em sudoers,
# unidades, deploy e nas tres suites de teste.
{ config, pkgs, lib, ... }:

let
  # Telas suportadas. A 1 sobe no boot; as demais entram por `screen-add`.
  screens = [ 1 2 3 4 5 6 7 8 9 ];

  # Caminho do volume duravel. Vale a pena repetir por que ele importa: e o
  # unico estado que sobrevive a reconstrucao da maquina.
  workspace = "/workspace";

  # Os auxiliares vem de arquivos ao lado, e nao embutidos aqui, para nao
  # precisarem de escape de `$` em string Nix. Sao ~140 linhas de shell com
  # `$1`, `$((...))` e `${1:?...}` -- escapar tudo isso e onde o erro se
  # esconderia, e o ganho seria zero.
  helper = name: pkgs.writeShellScriptBin name (builtins.readFile (./scripts + "/${name}.sh"));
in
{
  # google-chrome e unfree. Declarado no minimo possivel: so este pacote, e nao
  # um `allowUnfree = true` geral que abriria a porta para qualquer coisa entrar
  # sem ninguem notar.
  nixpkgs.config.allowUnfreePredicate = pkg:
    builtins.elem (lib.getName pkg) [ "google-chrome" ];

  # ---------------------------------------------------------------------------
  # Sistema base
  # ---------------------------------------------------------------------------

  # Rede de seguranca contra OOM do Chrome, que e o processo mais faminto da
  # maquina. 2 GB sobre os 4 GB de RAM, igual ao caminho do Ubuntu.
  swapDevices = [{
    device = "/swapfile";
    size = 2048;
  }];

  boot.kernel.sysctl."vm.swappiness" = 10;

  time.timeZone = "America/Sao_Paulo";

  # ---------------------------------------------------------------------------
  # Estado duravel
  # ---------------------------------------------------------------------------
  #
  # Montado por `by-id`, e NAO por label.
  #
  # A referencia da Tinnova monta por label porque a AWS renomeia o device
  # (nvme1n1 x sdf). No DigitalOcean o by-id ja E o identificador estavel, entao
  # o motivo de la nao existe aqui -- e trocar a identidade da montagem junto
  # com a troca de sistema seriam duas mudancas de uma vez.
  #
  # `nofail` substitui o laco de 30 tentativas do cloud-init: o boot nao trava se
  # o volume ainda nao apareceu, e as unidades que dependem dele usam
  # RequiresMountsFor.
  fileSystems.${workspace} = {
    device = "/dev/disk/by-id/scsi-0DO_Volume_agent-computer-workspace";
    fsType = "ext4";
    options = [ "discard" "defaults" "noatime" "nofail" ];
  };

  # ---------------------------------------------------------------------------
  # Usuarios -- a separacao que protege o cofre
  # ---------------------------------------------------------------------------
  #
  # agentd  dono da identidade do cofre    NAO executa nada do modelo
  # agent   dono do navegador e do trabalho EXECUTA tudo do modelo
  #
  # Sem essa separacao a cifra em repouso protegeria a foto do volume e nada
  # mais: o `bash -c` do modelo rodaria com o usuario dono da identidade age, e
  # um `cat` entregaria todos os segredos.
  users.mutableUsers = false;

  # uid e gid FIXOS, e nao os que o sistema escolher.
  #
  # O volume duravel e COMPARTILHADO entre os dois caminhos de deploy: a mesma
  # particao e montada por uma maquina Ubuntu ou por uma NixOS, conforme o
  # AGENT_OS. Numero de usuario e o que fica gravado no inode -- nome nao fica.
  #
  # Medido em 30/08/2026, e custou o provisionamento do cofre: os uid
  # coincidiram por acaso (1000 e 999 nos dois), mas os gid NAO -- o grupo
  # `agent` era 1000 no Ubuntu e virou 999 no NixOS, e `agentd` era 988 e virou
  # 998. O `ls` mostrava o grupo como NUMERO em vez de nome, e o efeito era
  # `permission denied` num diretorio que parecia perfeitamente correto.
  #
  # Os valores abaixo sao os do Ubuntu, porque o volume ja esta gravado com
  # eles. Mudar qualquer um destes numeros exige um `chown -R` no volume.
  users.groups.agent = { gid = 1000; };
  users.groups.agentd = { gid = 988; };

  # A chave de root e declarada AQUI, e nao deixada por conta do nixos-infect.
  #
  # Ele de fato embute a chave que o DigitalOcean injetou -- mas depender disso
  # e apostar o acesso a maquina no comportamento de um script de terceiro. A
  # avaliacao local recusou a configuracao sem esta linha, com a assercao
  # "Neither the root account nor any wheel user has a password or SSH
  # authorized key. You must set one to prevent being locked out": a checagem
  # barata cobrando o que teria custado um droplet inalcancavel.
  #
  # root por chave e a autoridade do OPERADOR: e por ela que o binario do
  # servico e instalado. O modelo nao alcanca essa chave por caminho nenhum.
  users.users.root.openssh.authorizedKeys.keyFiles = [ ./agent-authorized-keys ];

  users.users.agentd = {
    isSystemUser = true;
    uid = 999;
    group = "agentd";
    # Entra no grupo `agent` para escrever no /workspace compartilhado. O cofre
    # e a identidade ficam 0700, sem leitura de grupo: pertencer ao grupo da
    # acesso ao TRABALHO, nunca ao segredo.
    extraGroups = [ "agent" ];
    shell = "${pkgs.shadow}/bin/nologin";
  };

  users.users.agent = {
    isNormalUser = true;
    uid = 1000;
    group = "agent";
    home = "/home/agent";
    shell = pkgs.bashInteractive;
    # A chave vem de um arquivo AO LADO deste modulo, e nao de
    # /root/.ssh/authorized_keys.
    #
    # Os dois funcionariam na maquina, mas `keyFiles` e lido em tempo de
    # AVALIACAO -- e apontar para /root tornaria impossivel avaliar o modulo no
    # Mac antes de gastar um droplet. A avaliacao local e o que pega opcao
    # inexistente, tipo errado e atributo duplicado; perde-la para poupar um
    # arquivo seria trocar a verificacao barata pela cara.
    #
    # O conteudo e uma chave PUBLICA: viaja no user-data sem problema, e o
    # DigitalOcean ja a conhece.
    openssh.authorizedKeys.keyFiles = [ ./agent-authorized-keys ];
  };

  # ---------------------------------------------------------------------------
  # Privilegio -- lista fechada, validada NO BUILD
  # ---------------------------------------------------------------------------
  #
  # Este bloco e o argumento central da migracao. No Ubuntu, um erro de sintaxe
  # aqui faz o sudo DESCARTAR o drop-in inteiro em silencio, e o efeito nao e
  # "a regra nova nao vale" -- e "o agent perde todo o sudo". Aconteceu nesta
  # sessao, e o sintoma foi "a password is required" num comando sem relacao.
  # Aqui, erro de sintaxe nao compila.
  security.sudo = {
    enable = true;
    # Sem regra ampla para o grupo wheel: `agent` nao esta nele, e o unico
    # caminho e o que estiver escrito abaixo.
    wheelNeedsPassword = true;
    extraRules = [
      {
        # Rebaixamento: o servico abre mao de privilegio para rodar o que o
        # modelo pede. A direcao e sempre de REDUCAO -- agentd tem o cofre,
        # agent nao.
        users = [ "agentd" ];
        runAs = "agent";
        commands = [{ command = "ALL"; options = [ "NOPASSWD" "SETENV" ]; }];
      }
      {
        # Operacao pela conta SSH. Verbos explicitos: `systemctl` inteiro daria
        # root de volta por `edit`, `link` e `set-property`.
        #
        # Ficam FORA de proposito, cada um porque e root por outro caminho:
        #   nix-env, nixos-rebuild  -> instala derivacao arbitraria
        #   chown, chmod            -> muda o dono do arquivo de senha do cofre
        #   dd, tee, cp, cat, bash  -> leitura ou escrita arbitraria
        users = [ "agent" ];
        runAs = "root";
        commands = lib.map (c: { command = c; options = [ "NOPASSWD" ]; }) [
          "/run/current-system/sw/bin/systemctl start *"
          "/run/current-system/sw/bin/systemctl stop *"
          "/run/current-system/sw/bin/systemctl restart *"
          "/run/current-system/sw/bin/systemctl enable *"
          "/run/current-system/sw/bin/systemctl disable *"
          "/run/current-system/sw/bin/systemctl daemon-reload"
          "/run/current-system/sw/bin/systemctl reboot"
          "/run/current-system/sw/bin/systemctl status *"
          "/run/current-system/sw/bin/systemctl is-active *"
          "/run/current-system/sw/bin/systemctl list-timers *"
          "/run/current-system/sw/bin/journalctl -u agentd-api.service *"
          "/run/current-system/sw/bin/journalctl -u agentd-notify.service *"
          # `mount` mora nos WRAPPERS, e nao no perfil do sistema: ele e setuid,
          # e o NixOS poe binario setuid em /run/wrappers/bin. Apontar para o
          # perfil produz "sudo: a password is required" num comando permitido.
          "/run/wrappers/bin/mount -a"
          # O NixOS usa IPTABLES por padrao; `nft` fica para o caso de
          # networking.nftables ser ligado. Os dois sao LEITURA pura.
          "/run/current-system/sw/bin/iptables -S"
          "/run/current-system/sw/bin/iptables -S *"
          # pkill preso ao agentd: aberto, um `pkill sshd` derruba o acesso.
          "/run/current-system/sw/bin/pkill -9 -f agentd*"
        ];
      }
    ];
  };

  # ---------------------------------------------------------------------------
  # Diretorios e permissoes -- 10 passos do cloud-init viram declaracao
  # ---------------------------------------------------------------------------
  #
  # No Ubuntu isto era uma SEQUENCIA de `install -d` e `chown -R`, e foi ai que
  # `locks/` ficou com o dono antigo depois da separacao de usuarios. O
  # supervisor le falha de trava como "tela ocupada", entao TODA tela ficou em
  # 409 permanente com o disco limpo -- sintoma que nao aponta para permissao
  # em lugar nenhum. Aqui a permissao e reafirmada a cada boot.
  #
  # `z` em vez de `d` onde o diretorio ja existe no volume preservado: `d` so
  # cria, `z` tambem corrige dono e modo do que veio de uma versao anterior.
  systemd.tmpfiles.rules = [
    # Config do coletor eBPF, em /etc e NUNCA em /workspace.
    #
    # E o achado 3 da revisao aplicado antes de o defeito existir: um
    # EnvironmentFile em caminho gravavel pelo modelo foi a escalada que
    # desligou o rebaixamento das ferramentas. Aqui o modelo nao alcanca o
    # arquivo, e o binario o le sozinho -- sem shell na unidade (achado 4).
    "d /etc/agent-probe 0755 root root -"
    # O argumento final e o CONTEUDO: `f ... -` cria o arquivo vazio e o coletor
    # sobe sem destino -- captura, imprime no journal e nao envia. Engana porque
    # `is-active` diz active e ha evento no journal; so o backend fica sem dado.
    "f /etc/agent-probe/sink.url 0644 root root - http://127.0.0.1:9428/insert/jsonline?_stream_fields=source&_msg_field=_msg&_time_field=_time"
    # O efemero declarado, para a fronteira duravel x descartavel existir de fato.
    "d /scratch 1777 agent agent -"

    # Identidade do cofre: disco do SISTEMA, nunca no volume. E o que faz a foto
    # do volume ser inutil sozinha.
    "d /etc/agentd 0700 agentd agentd -"

    # O binario do servico. root:root -- quem escreve o binario e dono do
    # servico, e o servico e dono do cofre.
    "d /usr/local 0755 root root -"
    "d /usr/local/bin 0755 root root -"

    # Trabalho do modelo.
    "d ${workspace}/browser 0755 agent agent -"
    "d ${workspace}/projects 0755 agent agent -"

    # Estado do servico: do agentd, com o grupo agent apenas LENDO.
    #
    # ATENCAO: AS REGRAS ABAIXO NAO SAO APLICADAS, e isso foi medido em 30/08/2026:
    #
    #   Detected unsafe path transition /workspace (owned by agent)
    #   -> /workspace/agent (owned by agentd) during canonicalization
    #
    # O systemd-tmpfiles recusa atravessar mudanca de dono no meio do caminho.
    # Como `/workspace` e do `agent` e `/workspace/agent` do `agentd`, TODA
    # regra daqui para baixo e descartada -- em silencio, sem falhar a unidade.
    #
    # Quem de fato poe dono e permissao nestes diretorios e o oneshot
    # `agent-state-ownership`, logo abaixo. Elas ficam aqui como declaracao da
    # intencao e porque nao custam nada, mas NAO conte com elas: diretorio novo
    # sob /workspace/agent tem de ser criado la, nao aqui.
    "z ${workspace}/agent 2750 agentd agent -"
    "z ${workspace}/agent/locks 2750 agentd agent -"
    "z ${workspace}/agent/status 2750 agentd agent -"
    "z ${workspace}/agent/conversations 2750 agentd agent -"
    "z ${workspace}/agent/screenshots 2750 agentd agent -"

    # `screens/` e o UNICO estado do servico que o grupo agent ESCREVE (2770,
    # nao 2750): quem cria tela e `screen-add`, que roda como `agent`. Sem isto
    # ele morre em "mkdir: Permission denied" -- medido em 30/08/2026.
    #
    # O que impede isso de virar escalada: o nome do arquivo e o unico dado que
    # atravessa para o servico root, e ele passa por `case [2-9]` antes de
    # chegar ao `systemctl`. Nome fora disso e ignorado com aviso, entao nao ha
    # como injetar unidade. Tela, alem disso, nao e fronteira de seguranca no
    # desenho deste projeto -- quem alcanca uma alcanca o mesmo /workspace.
    "z ${workspace}/agent/tasks 0750 agentd agent -"
    "z ${workspace}/agent/events 0750 agentd agent -"

    # O cofre cifrado: 0700 do agentd. Pertencer ao grupo agent da acesso ao
    # trabalho, nunca ao segredo -- esta linha e a que separa as duas coisas.
    "z ${workspace}/agent/vault 0700 agentd agentd -"

    # O AGENTE NAO REESCREVE AS PROPRIAS REGRAS. Habilidades sao a instrucao
    # dele; conectores sao o alcance de rede dele. Do agentd, o modelo so le.
    "z ${workspace}/agent/skills 0755 agentd agentd -"
    "z ${workspace}/agent/connectors 0755 agentd agentd -"
    "z ${workspace}/agent/connectors/secrets 0700 agentd agentd -"

    # `claude` alcancavel pelo PATH.
    #
    # O prefixo do npm vive no volume duravel (para sobreviver ao `update`), e
    # esse caminho nao esta no PATH de ninguem. A ferramenta de delegacao procura
    # `claude` no PATH -- sem o link ela falha com "executable file not found",
    # que manda procurar na instalacao em vez de no PATH.
    #
    # Aqui e nao num `postStart` do servico: aquele roda como `agent`, que NAO
    # escreve em /usr/local/bin -- e nao deve mesmo, porque e onde mora o binario
    # do servico. O tmpfiles roda como root. Medido: o postStart falhou com
    # "Permission denied" e derrubou o servico inteiro, que ate entao tinha dado
    # certo.
    #
    # `L+` substitui um link que ja exista, para o alvo acompanhar uma
    # reinstalacao do npm.
    "L+ /usr/local/bin/claude - - - - ${workspace}/npm/bin/claude"
    # Os outros agentes de codigo, pelo mesmo caminho.
    #
    # `exec.LookPath` roda no contexto do agentd-api, cujo PATH nao inclui
    # /workspace/npm/bin. Sem o link, o runner cadastrado falha com "precisa de
    # <binario> no PATH" -- mensagem correta, mas para um binario que ESTA
    # instalado, o que manda procurar no lugar errado.
    #
    # Link em vez de mexer no PATH da unidade: a unidade ja teve o PATH
    # substituido por engano uma vez, e o npm quebrou com "enoent spawn sh".
    "L+ /usr/local/bin/codex - - - - ${workspace}/npm/bin/codex"
    "L+ /usr/local/bin/opencode - - - - ${workspace}/npm/bin/opencode"

    # Prefixo do npm no volume DURAVEL.
    #
    # Melhoria em relacao ao Ubuntu: la o agente de codigo era instalado no
    # disco do sistema e `task update` o perdia junto. Aqui ele sobrevive.
    "d ${workspace}/npm 0755 agent agent -"
  ];

  # ---------------------------------------------------------------------------
  # Segredos materializados no boot
  # ---------------------------------------------------------------------------
  #
  # Padrao da referencia da Tinnova: `oneshot` + `RemainAfterExit`, o segredo
  # nasce NA MAQUINA e nunca viaja no user-data.
  #
  # Idempotente de proposito -- a senha so e gerada se ainda nao existir. Gerar
  # de novo a cada boot orfanaria o cofre inteiro: a identidade age deixaria de
  # abrir, e os segredos continuariam no disco sem ninguem capaz de le-los.
  systemd.services.agentd-vault-passphrase = {
    description = "Gera a senha do cofre, uma vez e so uma vez";
    wantedBy = [ "multi-user.target" ];
    before = [ "agentd-api.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    path = [ pkgs.openssl ];
    script = ''
      if [ ! -s /etc/agentd/vault.pass ]; then
        umask 077
        openssl rand -base64 48 | tr -d '\n' > /etc/agentd/vault.pass
      fi
      chown agentd:agentd /etc/agentd/vault.pass
      chmod 600 /etc/agentd/vault.pass
    '';
  };

  # Normaliza a POSSE do estado depois de montar o volume.
  #
  # # Por que nao basta fixar uid e gid
  #
  # O volume e compartilhado entre os dois caminhos de deploy, e numero de grupo
  # e o que fica gravado no inode -- nome nao fica. A fixacao acima faz uma
  # maquina NOVA nascer com os mesmos numeros do Ubuntu, mas nao conserta um
  # volume que ja veio gravado com outros: o NixOS PRESERVA o gid de um grupo
  # que ja existe, e recusa muda-lo num rebuild.
  #
  # Medido em 30/08/2026: o `ls` mostrava o grupo como NUMERO em vez de nome
  # (`drwxr-s--- agentd 1000`), e o efeito era `permission denied` num diretorio
  # que parecia perfeitamente correto. O cofre nao provisionou por causa disso.
  #
  # # Por que `chown -R` e nao uma regra `Z` do tmpfiles
  #
  # `Z` recursivo aplicaria o MESMO modo a tudo, e `2750` num arquivo o deixa
  # executavel. Aqui so a posse e normalizada; o modo continua vindo das regras
  # `z`, uma por diretorio.
  #
  # Nao alcanca /workspace/browser de proposito: sao centenas de megabytes de
  # perfil do Chrome, ja pertencem a `agent`, e percorre-los a cada boot custaria
  # segundos sem consertar nada.
  systemd.services.agent-state-ownership = {
    description = "Normaliza a posse do estado no volume duravel";
    wantedBy = [ "multi-user.target" ];
    before = [ "agentd-api.service" "agentd-vault-passphrase.service" ];
    unitConfig.RequiresMountsFor = workspace;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    path = [ pkgs.coreutils ];
    script = ''
      # Sai em silencio se o volume nao montou: `nofail` permite a maquina subir
      # sem ele, e nesse caso nao ha estado para normalizar.
      [ -d ${workspace}/agent ] || exit 0
      # `screens/` guarda quais telas devem subir no boot. Criado AQUI, e nao
      # por tmpfiles: as regras de /workspace/agent sao recusadas por "unsafe
      # path transition" (ver o comentario la em cima).
      #
      # 2770 porque e o UNICO estado do servico que o grupo agent ESCREVE --
      # `screen-add` roda como `agent`. O nome do arquivo passa por `case
      # [2-9]` no agent-screens antes de virar argumento de systemctl, entao
      # nao ha unidade a injetar por ali.
      mkdir -p ${workspace}/agent/screens
      chown -R agentd:agent ${workspace}/agent
      # O cofre e as regras do agente sao do agentd SOZINHO -- pertencer ao
      # grupo da acesso ao trabalho, nunca ao segredo nem a propria instrucao.
      chown -R agentd:agentd ${workspace}/agent/vault ${workspace}/agent/skills ${workspace}/agent/connectors 2>/dev/null || true
      # api-token e a copia do OPERADOR, e volta a ser dele.
      #
      # O servidor le o token do COFRE (`origem=cofre` no log); este arquivo
      # existe so para o lado cliente, que roda como `agent`. A normalizacao
      # acima o engolia junto, e o cliente passava a receber "token ausente ou
      # invalido" -- com o arquivo ali, intacto, e o servico funcionando
      # perfeitamente.
      #
      # Custou cinco falhas em cascata numa suite: uma causa, cinco sintomas em
      # secoes diferentes.
      [ -e ${workspace}/agent/api-token ] && chown agent:agent ${workspace}/agent/api-token
      # As travas de tela precisam ser gravaveis pelo GRUPO: o servico e o CLI
      # do operador rodam como usuarios diferentes e os dois legitimamente as
      # tomam. Arquivo criado por uma versao anterior chega aqui com 0644.
      chmod g+w ${workspace}/agent/locks/*.lock 2>/dev/null || true
      chmod 2770 ${workspace}/agent/screens

      # Casa dos agentes de codigo alternativos.
      #
      # Dono `agent`, e nao `agentd`: quem roda o CLI e o usuario rebaixado, e
      # cada um deles quer escrever config, cache e sessao no HOME. Sem isto o
      # Codex falhou com "Failed to read config file ... Permission denied" --
      # o diretorio existia, criado pelo agentd, e o processo rebaixado nao
      # escrevia nele.
      #
      # Um subdiretorio por runner: cada CLI guarda credencial e sessao no HOME,
      # e misturar faria a configuracao de um aparecer para o outro.
      mkdir -p ${workspace}/agent/runner-home
      chown agentd:agent ${workspace}/agent/runner-home
      # 2770: o `agentd` CRIA o subdiretorio de cada runner, e o `agent` ESCREVE
      # dentro dele. Os dois precisam, e nenhum sozinho basta:
      #
      #   o agentd cria    porque e ele quem monta a chamada
      #   o agent escreve  porque o CLI roda rebaixado, e guarda config no HOME
      #
      # O setgid (o 2) faz o subdiretorio herdar o grupo `agent`, sem o que o
      # filho sairia com grupo `agentd` e o CLI voltaria a levar "permission
      # denied" -- com o diretorio existindo, que e o diagnostico mais confuso.
      chmod 2770 ${workspace}/agent/runner-home

      # Os quatro arquivos de memoria (guardrails, progresso, atividade, erros)
      # e o catalogo de runners.
      #
      # 0640 do agentd: o grupo `agent` LE, para o operador conferir sem virar
      # root, e NAO escreve. A distincao e a contencao inteira -- `guardrails.md`
      # entra no prompt de sistema de toda tarefa, e quem escreve o proprio
      # prompt de contencao nao esta contido. Mesma razao de `skills/`.
      #
      # Criados vazios aqui em vez de por `systemd.tmpfiles.rules`: as regras
      # sob /workspace/agent sao recusadas com "unsafe path transition" e
      # descartadas em silencio (ver o comentario la em cima).
      for arquivo in guardrails.md progress.md activity.log errors.log runners.json pricing.json; do
        caminho="${workspace}/agent/$arquivo"
        [ -e "$caminho" ] || : > "$caminho"
        chown agentd:agent "$caminho"
        chmod 0640 "$caminho"
      done

      # O catalogo nasce com os cinco runners cadastrados.
      #
      # So o `claude` esta instalado hoje; os outros ficam cadastrados de
      # proposito, e pedir um deles falha dizendo qual binario falta. E melhor
      # que omiti-los: a mensagem vira a documentacao de como instalar.
      if [ ! -s "${workspace}/agent/runners.json" ]; then
        cat > "${workspace}/agent/runners.json" <<'CATALOGO'
{
  "claude": {"cmd": ["claude", "-p", "--dangerously-skip-permissions", "{prompt}"],
             "env_file": "anthropic.env",
             "description": "Claude Code -- instalado e exercitado pela suite"},
  "codex":  {"cmd": ["codex", "exec", "--yolo", "--skip-git-repo-check", "-"],
             "stdin": true, "env_file": "openai.env",
             "description": "OpenAI Codex -- instalado"},
  "opencode": {"cmd": ["opencode", "run", "--model", "openrouter/x-ai/grok-4.6", "{prompt}"],
             "env_file": "openrouter.env",
             "description": "OpenCode -- instalado, via OpenRouter"},
  "droid":  {"cmd": ["droid", "exec", "--skip-permissions-unsafe", "-f", "{prompt}"],
             "description": "Factory Droid -- NAO instalado: nao esta no npm, so por script proprio"},
  "kiro":   {"cmd": ["kiro", "exec", "{prompt}"],
             "description": "Kiro -- NAO instalado: e IDE da AWS, sem CLI headless conhecido"}
}
CATALOGO
        chown agentd:agent "${workspace}/agent/runners.json"
        chmod 0640 "${workspace}/agent/runners.json"
      fi

      # A tabela de precos, para o teto de custo.
      #
      # Fica em ARQUIVO e nao no binario porque preco envelhece: tabela dentro
      # do codigo so se corrige recompilando, e uma tabela velha e pior que
      # nenhuma -- o teto passa a cortar no lugar errado e o numero parece
      # medido.
      #
      # A origem de cada entrada vai junto, com a data. Numero de preco sem
      # procedencia nao se confere.
      #
      # ATENCAO ao limiar de 200 mil tokens de prompt: acima dele a xAI COBRA O
      # DOBRO, entrada e saida. Um agente com historico longo cruza essa linha
      # sem avisar.
      if [ ! -s "${workspace}/agent/pricing.json" ]; then
        cat > "${workspace}/agent/pricing.json" <<'PRECOS'
{
  "grok-4.6": {
    "small_prompt": {"input_per_1m": 2.00, "cached_per_1m": 0.50, "output_per_1m": 6.00},
    "large_prompt": {"input_per_1m": 4.00, "cached_per_1m": 1.00, "output_per_1m": 12.00},
    "source": "docs.x.ai/docs/models, consultado em 2026-08-31"
  },
  "grok-4.5": {
    "small_prompt": {"input_per_1m": 2.00, "cached_per_1m": 0.30, "output_per_1m": 6.00},
    "large_prompt": {"input_per_1m": 4.00, "cached_per_1m": 0.60, "output_per_1m": 12.00},
    "source": "docs.x.ai/docs/models, consultado em 2026-08-31"
  },
  "grok-4.3": {
    "small_prompt": {"input_per_1m": 1.25, "cached_per_1m": 0.20, "output_per_1m": 2.50},
    "large_prompt": {"input_per_1m": 2.50, "cached_per_1m": 0.40, "output_per_1m": 5.00},
    "source": "docs.x.ai/docs/models, consultado em 2026-08-31"
  }
}
PRECOS
        chown agentd:agent "${workspace}/agent/pricing.json"
        chmod 0640 "${workspace}/agent/pricing.json"
      fi
      true
    '';
  };

  # Marcador que o `agent-status` le para dizer se o estado e duravel.
  systemd.services.agent-volume-marker = {
    description = "Registra se /workspace veio do volume separado";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      if ${pkgs.util-linux}/bin/mountpoint -q ${workspace}; then
        echo VOLUME_MONTADO > /var/lib/agent-computer-volume
      else
        echo SEM_VOLUME > /var/lib/agent-computer-volume
      fi
      echo READY > /var/lib/agent-computer-ready
    '';
  };

  # Agente de codigo, para a delegacao. Instalado no prefixo DURAVEL, entao o
  # passo so faz trabalho na primeira vez.
  systemd.services.agent-code-tool = {
    description = "Instala o agente de codigo no prefixo duravel";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    requires = [ "workspace.mount" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      User = "agent";
      Group = "agent";
    };
    # `path` SUBSTITUI o PATH inteiro em NixOS -- nao acrescenta.
    #
    # Com so nodejs e curl a instalacao falhou com `enoent spawn sh ENOENT`: os
    # scripts de pos-instalacao do npm precisam de um shell, e nao havia
    # nenhum. O erro nao diz "PATH incompleto", diz que nao achou um arquivo --
    # e manda procurar no pacote errado.
    path = [ pkgs.nodejs_22 pkgs.curl pkgs.bash pkgs.coreutils pkgs.gnutar pkgs.gzip ];
    environment.NPM_CONFIG_PREFIX = "${workspace}/npm";
    script = ''
      if [ ! -x ${workspace}/npm/bin/claude ]; then
        npm install -g @anthropic-ai/claude-code || exit 1
      fi
    '';

  };

  # ---------------------------------------------------------------------------
  # Telas -- Xvfb, Openbox, VNC, noVNC e Chrome, uma instancia por tela
  # ---------------------------------------------------------------------------
  systemd.services."xvfb@" = {
    description = "Servidor X virtual - tela %i";
    after = [ "network.target" ];
    serviceConfig = {
      User = "agent";
      ExecStart = "${pkgs.xorg.xorgserver}/bin/Xvfb :%i -screen 0 1920x1080x24 -ac -nolisten tcp";
      Restart = "always";
      RestartSec = 2;
    };
  };

  systemd.services."openbox@" = {
    description = "Gerenciador de janelas Openbox - tela %i";
    after = [ "xvfb@%i.service" ];
    requires = [ "xvfb@%i.service" ];
    environment.DISPLAY = ":%i";
    serviceConfig = {
      User = "agent";
      ExecStart = "${pkgs.openbox}/bin/openbox --sm-disable";
      Restart = "always";
      RestartSec = 2;
    };
  };

  systemd.services."x11vnc@" = {
    description = "Servidor VNC - tela %i";
    after = [ "openbox@%i.service" ];
    requires = [ "xvfb@%i.service" ];
    environment.DISPLAY = ":%i";
    serviceConfig = {
      User = "agent";
      # `localhost` de proposito: o acesso e pelo tunel SSH que ja serve a tela.
      # A porta e 5900 + numero da tela, calculada pelo shell porque o systemd
      # nao faz aritmetica com o especificador %i.
      ExecStart = ''${pkgs.bash}/bin/bash -c "${pkgs.x11vnc}/bin/x11vnc -display :%i -localhost -nopw -forever -shared -rfbport $((5900 + %i)) -noxdamage"'';
      Restart = "always";
      RestartSec = 2;
    };
  };

  systemd.services."novnc@" = {
    description = "noVNC - tela %i";
    after = [ "x11vnc@%i.service" ];
    requires = [ "x11vnc@%i.service" ];
    serviceConfig = {
      User = "agent";
      ExecStart = ''${pkgs.bash}/bin/bash -c "${pkgs.python3Packages.websockify}/bin/websockify --web=${pkgs.novnc}/share/webapps/novnc 127.0.0.1:$((6080 + %i)) 127.0.0.1:$((5900 + %i))"'';
      Restart = "always";
      RestartSec = 2;
    };
  };

  systemd.services."chrome@" = {
    description = "Google Chrome persistente - tela %i";
    after = [ "openbox@%i.service" ];
    requires = [ "xvfb@%i.service" ];
    unitConfig.RequiresMountsFor = workspace;
    environment.DISPLAY = ":%i";
    serviceConfig = {
      User = "agent";
      ExecStart = lib.concatStringsSep " " [
        "${pkgs.google-chrome}/bin/google-chrome-stable"
        "--user-data-dir=${workspace}/browser/screen-%i"
        "--remote-debugging-port=922%i"
        "--remote-debugging-address=127.0.0.1"
        "--window-position=0,0"
        "--window-size=1920,1080"
        "--start-maximized"
        "--no-first-run"
        "--no-default-browser-check"
        "--disable-session-crashed-bubble"
        "--disable-features=Translate,MediaRouter"
        # `basic` e o que permite a semeadura de sessao entre telas funcionar:
        # com o chaveiro do usuario, a copia do perfil produziria cookies que
        # nao decifram, e o sintoma seria "deslogado" sem erro nenhum.
        "--password-store=basic"
      ];
      Restart = "always";
      RestartSec = 5;
    };
  };

  # A tela 1 sobe no boot; as demais entram por `screen-add`.
  systemd.targets.multi-user.wants = [
    "xvfb@1.service"
    "openbox@1.service"
    "x11vnc@1.service"
    "novnc@1.service"
    "chrome@1.service"
  ];

  # Telas 2..9: quem manda e o marcador no volume duravel, nao `systemctl
  # enable`.
  #
  # No NixOS /etc/systemd/system e READ-ONLY -- aponta para o store. O
  # `enable` de uma instancia templada precisa gravar um symlink ali e falha
  # com "Read-only file system". Medido em 30/08/2026: `screen-add 2` saia com
  # rc=1 e nenhuma unidade da tela subia.
  #
  # Poe a fonte de verdade onde ela ja deveria estar: o volume. A tela criada
  # sobrevive ao rebuild do sistema E a troca de SO, porque o marcador nao mora
  # no disco do droplet.
  systemd.services.agent-screens = {
    description = "agent computer - sobe as telas marcadas no volume duravel";
    after = [ "workspace.mount" "chrome@1.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    path = with pkgs; [ systemd coreutils ];
    script = ''
      # Sem marcador nenhum, nao ha o que fazer -- e isso e o normal.
      [ -d /workspace/agent/screens ] || exit 0
      for marker in /workspace/agent/screens/*; do
        [ -e "$marker" ] || continue
        screen="$(basename "$marker")"
        case "$screen" in
          [2-9]) ;;
          *) echo "marcador ignorado (fora de 2..9): $screen"; continue ;;
        esac
        echo "subindo a tela $screen"
        # `|| true` de proposito: uma tela que nao sobe nao pode impedir as
        # outras, nem deixar a unidade em falha e travar o multi-user.target.
        systemctl start \
          "xvfb@$screen" "openbox@$screen" "x11vnc@$screen" \
          "novnc@$screen" "chrome@$screen" || true
      done
    '';
  };

  # ---------------------------------------------------------------------------
  # Porta HTTP de tarefas e entrega de avisos
  # ---------------------------------------------------------------------------
  systemd.services.agentd-api = {
    description = "agent computer - porta HTTP de tarefas";
    after = [ "network-online.target" "agentd-vault-passphrase.service" ];
    wants = [ "network-online.target" ];
    requires = [ "agentd-vault-passphrase.service" ];
    # SEM ISTO A PORTA NAO SOBE NO BOOT, e o modo de falha e mudo.
    #
    # `after` e `requires` so dizem ORDEM e dependencia -- nao fazem nada
    # comecar. Sem alguem que a QUEIRA, a unidade fica declarada e parada:
    # `systemctl is-enabled` responde "linked" em vez de "enabled", nenhuma
    # unidade entra em falha, e o `systemctl status` do sistema segue
    # "running".
    #
    # Medido em 30/08/2026: depois de um reboot, o agentd-api ficou parado por
    # 26 minutos, ate uma suite inicia-lo por acaso. A suite HTTP reprovou com
    # NOVE erros em cascata -- porta sem escutar, health vazio, 000 no lugar de
    # 401, criacao de tarefa falhando -- todos de uma causa so.
    #
    # No Ubuntu isto vinha do `systemctl enable` no cloud-init. A migracao para
    # NixOS trouxe a unidade e deixou o `enable` para tras.
    wantedBy = [ "multi-user.target" ];
    unitConfig.RequiresMountsFor = workspace;

    # Telemetria. 4317 = VictoriaTraces (gRPC, trechos); 8428 = VictoriaMetrics
    # (HTTP, metricas). BACKENDS DIFERENTES: apontar os dois para 4317 devolve
    # `Unimplemented: MetricsService/Export`, que parece erro de rede e nao e.
    #
    # Sao enderecos de LOOPBACK e chegam ao Mac pelo tunel reverso, entao a
    # maquina segue sem escutar nada alem da 22 (invariante do 08-validate).
    # Mac fora do ar nao derruba a tarefa: o exportador e em lote e nunca
    # bloqueia (TestNewWithEndpointDoesNotBlockOnDial).
    environment = {
      AGENTD_OTLP_ENDPOINT = "127.0.0.1:4317";
      AGENTD_OTLP_METRICS_ENDPOINT = "127.0.0.1:8428";
      AGENTD_LOG_LEVEL = "info";
    };

    serviceConfig = {
      User = "agentd";
      Group = "agentd";
      # 127.0.0.1 de proposito: o acesso e pelo mesmo tunel SSH que ja serve a
      # tela. Escutar em 0.0.0.0 esta a uma regra de firewall mal escrita de
      # estar na internet publica, e o modo de falha e silencioso.
      ExecStart = "/usr/local/bin/agentd -serve -listen 127.0.0.1:8787";
      Restart = "on-failure";
      RestartSec = 10;
      # O encerramento limpo cancela as tarefas em voo, elas gravam o estado e
      # soltam a trava. 40s da folga para isso antes do SIGKILL.
      TimeoutStopSec = 40;
      KillSignal = "SIGTERM";

      # Isolamento. Vale em dobro aqui porque as ferramentas do MODELO herdam
      # este namespace: o que esta fechado para o servico esta fechado para ele.
      ProtectSystem = "strict";
      ReadWritePaths = [ workspace "/scratch" ];
      PrivateTmp = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectControlGroups = true;
      RestrictRealtime = true;
      LockPersonality = true;
      # NoNewPrivileges e RestrictSUIDSGID ficam DE FORA, e nao por descuido: o
      # rebaixamento das ferramentas usa sudo, que e setuid. Liga-los quebraria
      # justamente o mecanismo que tira o cofre do alcance do modelo -- a
      # protecao maior perderia para a menor.
      NoNewPrivileges = false;
    };
    environment = {
      # AGENTD_TOOL_USER e o que faz o cofre valer contra quem ja esta dentro da
      # maquina: toda ferramenta que o modelo dispara cai para este usuario, que
      # nao le a identidade age.
      #
      # Aqui nao existe a armadilha do Ubuntu, em que um EnvironmentFile listado
      # DEPOIS sobrescrevia esta variavel: nao ha EnvironmentFile, e o valor e
      # parte da mesma expressao que cria a unidade.
      AGENTD_TOOL_USER = "agent";
      PATH = lib.mkForce "/run/current-system/sw/bin:${workspace}/npm/bin";
    };
  };

  # Coletor eBPF: registra no kernel o que o modelo executa.
  #
  # E a camada que o agentd instrumentado nao consegue ser. O agente sabe a
  # INTENCAO e e cego ao que o sistema fez; esta sabe o FATO e e cega a
  # intencao. So esta vale como PROVA, porque roda no kernel e o usuario `agent`
  # nao a desliga.
  #
  # O NOME e decisao de seguranca: `agent-probe`, nunca `agentd-probe`. O
  # sudoers concede ao `agent` -- logo ao modelo -- `pkill -9 -f agentd*`, e o
  # segundo nome casaria com esse padrao. Pelo mesmo motivo esta unidade fica
  # FORA das duas cujo journal o `agent` pode ler: o auditado nao le o log da
  # auditoria.
  systemd.services.agent-probe = {
    description = "agent computer - coletor eBPF (somente observacao)";
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    # `wantedBy` EXPLICITO. A falta desta linha no agentd-api deixou o servico
    # 26 minutos fora do ar depois de um reboot, sem nenhuma unidade em falha.
    # Num coletor de auditoria o mesmo defeito e pior: ele nao coleta, e nada
    # aponta para isso.
    wantedBy = [ "multi-user.target" ];
    # SEM RequiresMountsFor=/workspace, de proposito: o coletor nao grava no
    # volume. Amarra-lo a montagem o faria depender do que ele observa.

    serviceConfig = {
      # User=root, e a decisao foi MEDIDA, nao presumida.
      #
      # O `cilium/ebpf` atacha tracepoint por `perf_event_open`, e o id do
      # tracepoint vem de LER /sys/kernel/tracing/events/<g>/<n>/id. Nesta
      # maquina esse diretorio e 700 root:root, e o usuario `agent` recebe
      # "Permission denied" (medido em 31/08/2026). Isso e checagem de DAC, nao
      # de capacidade: CAP_PERFMON nao abre arquivo sem permissao.
      #
      # A alternativa seria um usuario proprio com CAP_DAC_READ_SEARCH -- que le
      # QUALQUER arquivo da maquina, inclusive /etc/agentd/vault.pass. Os dois
      # leem tudo; so um admite. Root com bounding set apertado e o honesto.
      User = "root";
      CapabilityBoundingSet = [ "CAP_BPF" "CAP_PERFMON" "CAP_DAC_READ_SEARCH" ];
      # Seguro AQUI, ao contrario do agentd-api: esta unidade nao usa sudo e nao
      # executa nada alem do proprio binario.
      NoNewPrivileges = true;

      # SEM SHELL no ExecStart. E o achado 4 da revisao: o `sh -c` da unidade de
      # avisos era injecao de comando.
      ExecStart = "/usr/local/bin/agent-probe -sink-file /etc/agent-probe/sink.url";
      Restart = "always";
      RestartSec = 5;

      # Teto de recurso: o coletor NUNCA pode ser a causa da degradacao que ele
      # mede. Com 96M ele morre e reinicia em vez de comer a folga do Chrome.
      MemoryMax = "96M";
      CPUQuota = "15%";
      Nice = 10;

      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      ProtectKernelModules = true;
      # ProtectControlGroups fica em FALSE: o coletor precisa ler /sys/fs/cgroup
      # para traduzir o id numerico do cgroup em nome de unidade -- que e o que
      # distingue o que o agentd disparou do que o Chrome disparou. Nesta
      # maquina o uid NAO faz essa distincao: os dois rodam como `agent`.
      ProtectControlGroups = false;
      RestrictNamespaces = true;
      RestrictRealtime = true;
      LockPersonality = true;
      RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" "AF_NETLINK" ];
      SystemCallArchitectures = "native";
    };
  };

  systemd.services.agentd-notify = {
    description = "agent computer - entrega os avisos enfileirados";
    # `wants` junto com `after`: ordenar sem depender faz o systemd avisar, e o
    # efeito real seria o drenador rodar antes de haver rede e falhar a entrega
    # em silencio -- que e exatamente o modo de falha que a proatividade nao
    # pode ter.
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    unitConfig.RequiresMountsFor = workspace;
    serviceConfig = {
      Type = "oneshot";
      # agentd, e nao agent: a fila e ESCRITA pelo servico, e um leitor com
      # outro usuario recebe "permission denied". No Ubuntu esta unidade ficou
      # para tras na separacao de usuarios e a proatividade inteira quebrou EM
      # SILENCIO -- os avisos eram enfileirados e nunca sairiam.
      User = "agentd";
      Group = "agentd";
      # SEM shell. O destino vinha de arquivo que o modelo escrevia, interpolado
      # entre aspas dentro de `sh -c`: um valor fechando a aspa emendaria outro
      # comando. O binario le AGENT_WEBHOOK do ambiente ele mesmo.
      ExecStart = "/usr/local/bin/agentd -notify-drain";
      # O prefixo `-` torna o arquivo opcional: sem ele, o drenador so lista os
      # avisos pendentes e nao consome a fila.
      EnvironmentFile = "-/etc/agentd/notify.env";
    };
  };

  systemd.timers.agentd-notify = {
    description = "Dispara a entrega dos avisos";
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnBootSec = "2min";
      OnUnitActiveSec = "5min";
      AccuracySec = "10s";
    };
  };

  # ---------------------------------------------------------------------------
  # Rede
  # ---------------------------------------------------------------------------
  #
  # Desfaz a rota IPv6 VAZIA que o instalador gera.
  #
  # Ele escreve `defaultGateway6 = { address = ""; }` e uma rota `{ address =
  # ""; prefixLength = 128; }` mesmo quando o droplet nao tem IPv6 -- e o
  # resultado e `ip route add "/128"`, que falha com "any valid prefix is
  # expected".
  #
  # A rede IPv4 sobe normalmente, entao a maquina funciona. O custo e outro, e e
  # o que importa: `systemctl is-system-running` fica em `degraded` PARA SEMPRE.
  # Um sistema permanentemente degradado nao serve de sinal de saude -- a
  # proxima falha de verdade se esconde no mesmo ruido, e o
  # `31-nixos-rebuild.sh` usa exatamente esse sinal para dizer se um deploy deu
  # certo.
  #
  # `eth0` fixo porque e o nome no DigitalOcean; noutro provedor isto precisaria
  # olhar a interface de verdade.
  networking.defaultGateway6 = lib.mkForce null;
  networking.interfaces.eth0.ipv6.routes = lib.mkForce [ ];
  #
  # Substitui o ufw. Nada alem do SSH entra: as portas de tela (5901, 6081) e a
  # de tarefas (8787) sao alcancadas pelo tunel SSH, nunca pela internet.
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [ 22 ];
  };

  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      # root por chave e a autoridade do OPERADOR, e e o que instala o binario
      # do servico. O modelo nao alcanca essa chave por caminho nenhum -- ela
      # existe so no Mac.
      PermitRootLogin = "prohibit-password";
    };
  };

  services.tailscale.enable = true;

  # nix-ld: faz binario PRE-COMPILADO de terceiro rodar.
  #
  # O agente de codigo vem do npm como executavel ja compilado, ligado
  # dinamicamente contra a glibc e procurando `/lib64/ld-linux-x86-64.so.2`.
  # O NixOS nao tem esse caminho -- cada binario aponta para o carregador dentro
  # do proprio /nix/store -- e a mensagem e literal:
  #
  #   Could not start dynamically linked executable: claude
  #
  # O `nix-ld` poe um carregador compativel no caminho padrao, so para esse tipo
  # de programa. E a resposta idiomatica do NixOS para software distribuido em
  # binario, e nao ha alternativa sem empacotar o agente de codigo como
  # derivacao -- trabalho a parte, e que teria de acompanhar cada versao dele.
  #
  # O binario do `agentd` NAO precisa disto: e Go estatico (CGO_ENABLED=0).
  programs.nix-ld.enable = true;
  programs.nix-ld.libraries = with pkgs; [ stdenv.cc.cc.lib zlib openssl ];

  # /usr/local/bin no PATH.
  #
  # E onde moram o binario do servico e o link do agente de codigo. O NixOS nao
  # o inclui por padrao -- o que e coerente com a filosofia dele, e aqui produz
  # um `claude: command not found` num link que existe e esta correto.
  environment.extraInit = ''
    export PATH="$PATH:/usr/local/bin"
  '';

  # ---------------------------------------------------------------------------
  # Pacotes e fontes
  # ---------------------------------------------------------------------------
  environment.systemPackages = with pkgs; [
    git curl jq tmux htop unzip gnupg
    # python3 NAO vem de fabrica aqui, ao contrario do Ubuntu.
    #
    # E preciso por dois motivos: o agente escreve codigo Python e precisa
    # roda-lo, e varios scripts de verificacao usam `python3 -c` na maquina. A
    # ausencia apareceu com "python3: command not found" DENTRO da verificacao
    # do teste de delegacao -- que, pior, devolveu rc=0 assim mesmo.
    python3
    xdotool scrot xorg.xdpyinfo
    nodejs_22
    (helper "screen-add")
    (helper "screen-remove")
    (helper "session-sync")
    (helper "agent-status")
  ];

  # Sem fonte, o Chrome desenha caixas no lugar de texto e a captura de tela
  # parece funcionar -- e o defeito so aparece quando alguem olha o PNG.
  fonts.packages = with pkgs; [
    liberation_ttf
    noto-fonts
    noto-fonts-color-emoji
    dejavu_fonts
  ];

  # Versao do estado. NAO acompanha a versao do NixOS: mudar isto sem ler as
  # notas de migracao e como a maioria dos danos silenciosos acontece.
  #
  # `mkForce` porque o instalador fixa "23.11" na configuration.nix que ele
  # gera, e duas definicoes de mesma prioridade nao se resolvem sozinhas -- a
  # construcao PARA com "conflicting definition values". Custou uma conversao
  # inteira descobrir; o verificador local passou a replicar o valor dele
  # justamente para o conflito aparecer aqui.
  #
  # 25.11 e o valor correto: esta maquina nasce agora, nao foi migrada de uma
  # instalacao antiga. E o estado que importa nao mora no disco do sistema de
  # qualquer forma -- ele esta no volume.
  system.stateVersion = lib.mkForce "25.11";
}
