# Configuracao declarativa do agent-computer em NixOS.
#
# Importado pelo nixos-infect via NIXOS_IMPORT, entao o `configuration.nix` e o
# `hardware-configuration.nix` continuam sendo os que ele gera -- com a rede do
# DigitalOcean e a chave SSH de root embutidas. Este arquivo NAO os substitui.
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

  users.groups.agent = { };
  users.groups.agentd = { };

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
    group = "agentd";
    # Entra no grupo `agent` para escrever no /workspace compartilhado. O cofre
    # e a identidade ficam 0700, sem leitura de grupo: pertencer ao grupo da
    # acesso ao TRABALHO, nunca ao segredo.
    extraGroups = [ "agent" ];
    shell = "${pkgs.shadow}/bin/nologin";
  };

  users.users.agent = {
    isNormalUser = true;
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
          "/run/current-system/sw/bin/mount -a"
          "/run/current-system/sw/bin/nft list ruleset"
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
    "z ${workspace}/agent 2750 agentd agent -"
    "z ${workspace}/agent/locks 2750 agentd agent -"
    "z ${workspace}/agent/status 2750 agentd agent -"
    "z ${workspace}/agent/conversations 2750 agentd agent -"
    "z ${workspace}/agent/screenshots 2750 agentd agent -"
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
    path = [ pkgs.nodejs_22 pkgs.curl ];
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

  # ---------------------------------------------------------------------------
  # Porta HTTP de tarefas e entrega de avisos
  # ---------------------------------------------------------------------------
  systemd.services.agentd-api = {
    description = "agent computer - porta HTTP de tarefas";
    after = [ "network-online.target" "agentd-vault-passphrase.service" ];
    wants = [ "network-online.target" ];
    requires = [ "agentd-vault-passphrase.service" ];
    unitConfig.RequiresMountsFor = workspace;
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

  # ---------------------------------------------------------------------------
  # Pacotes e fontes
  # ---------------------------------------------------------------------------
  environment.systemPackages = with pkgs; [
    git curl jq tmux htop unzip gnupg
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
  # `mkForce` porque o nixos-infect fixa "23.11" na configuration.nix que ele
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
