# Diagnóstico de site fora do ar

Procedimento para quando alguém diz que um endereço "não está funcionando".
Trabalhe de fora para dentro: cada camada elimina metade das causas.

## 1. Confirme o sintoma antes de investigar

```
curl -sS -o /dev/null -w 'http=%{http_code} tempo=%{time_total}s ip=%{remote_ip}\n' -L https://<host>
```

Anote o código. "Fora do ar" pode ser 200 lento, 403 de bloqueio ou DNS que nem
resolve — e cada um leva a um caminho completamente diferente.

## 2. Separe DNS de aplicação

```
dig +short <host>
dig +short <host> @1.1.1.1
```

Respostas diferentes entre o resolvedor local e o público significam propagação
ou cache, não aplicação caída. Nenhuma resposta é DNS, e aí nada adianta olhar
o servidor.

## 3. Separe rede de aplicação

```
curl -sS -o /dev/null -w 'conectou em %{time_connect}s, TLS em %{time_appconnect}s\n' https://<host>
```

Se o TCP conecta e o TLS completa, a rede está boa e o problema é da aplicação.
Se nem conecta, é firewall, rota ou serviço parado.

## 4. Só então olhe o servidor

```
systemctl status <servico>
journalctl -u <servico> -n 50 --no-pager
df -h /
```

Inclua o disco: serviço que morre sem explicação com frequência é disco cheio,
e o log dessa falha costuma ser a última coisa que consegue ser escrita.

## Regras deste procedimento

- **Meça antes de mexer.** Registre o estado antes de qualquer alteração, senão
  não há como saber se o que você fez ajudou.
- **Uma mudança por vez**, com nova medição entre elas. Duas mudanças juntas e
  a melhora não tem dono.
- **Não reinicie para "ver se resolve"** antes de coletar o log: o reinício
  apaga a prova do que causou.
