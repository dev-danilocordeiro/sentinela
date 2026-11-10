#!/bin/sh
# Gera e ativa as units do Sentinela a partir dos modelos de deploy/systemd. NÃO é rodado
# automaticamente: leia docs/operacao.md ("Rodando sozinho") antes.
#
#   sudo deploy/install.sh               units de sistema em /etc/systemd/system, com User=sentinela
#                                        e o Sentinela em /opt/sentinela
#   deploy/install.sh --user             systemd do usuário atual (~/.config/systemd/user), com o
#                                        Sentinela no diretório deste repo
#   deploy/install.sh --render DIR [--user]
#                                        só gera as units em DIR, sem systemctl (para conferir)
#   deploy/install.sh --uninstall [--user]
#   --prefix NOME                        nome das units (padrão sentinela): NOME-serve.service,
#                                        NOME-worker.service, NOME-scan.service e NOME-scan.timer.
#                                        Para uma segunda instalação na mesma máquina (ADR 0056)
#
# Variáveis: SENTINELA_HOME (diretório do Sentinela; padrão /opt/sentinela, ou este repo com
# --user), SENTINELA_USER (usuário de serviço das units de sistema; padrão sentinela) e
# SENTINELA_UNIT_PREFIX (o mesmo que --prefix).
#
# Nunca sobrescreve nem remove a unit de outra instalação: se a unit com o mesmo nome já existe e
# o SENTINELA_HOME dela é outro, para e pede um --prefix diferente.
set -eu
DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$DIR/.." && pwd)"
# Modelos em deploy/systemd; o nome gerado troca o "sentinela" do começo pelo prefixo.
TEMPLATES="sentinela-serve.service sentinela-worker.service sentinela-scan.service sentinela-scan.timer"

user_mode=false
render=""
uninstall=false
prefix="${SENTINELA_UNIT_PREFIX:-sentinela}"
while [ $# -gt 0 ]; do
	case "$1" in
	--user) user_mode=true ;;
	--uninstall) uninstall=true ;;
	--render)
		[ $# -ge 2 ] || { echo "--render precisa de um diretório" >&2; exit 2; }
		render="$2"
		shift
		;;
	--prefix)
		[ $# -ge 2 ] || { echo "--prefix precisa de um nome" >&2; exit 2; }
		prefix="$2"
		shift
		;;
	*) echo "opção desconhecida: $1" >&2; exit 2 ;;
	esac
	shift
done
case "$prefix" in
'' | -* | *[!a-z0-9-]*) echo "prefixo inválido: \"$prefix\" (use minúsculas, dígitos e -, ex.: ceres-sentinela)" >&2; exit 2 ;;
esac
SERVICES="$prefix-serve.service $prefix-worker.service $prefix-scan.timer"

if $user_mode; then
	home_dir="${SENTINELA_HOME:-$REPO}"
	dest="$HOME/.config/systemd/user"
	wanted="default.target"
	user_line=""
	ctl="systemctl --user"
else
	home_dir="${SENTINELA_HOME:-/opt/sentinela}"
	dest="/etc/systemd/system"
	wanted="multi-user.target"
	user_line="User=${SENTINELA_USER:-sentinela}"
	ctl="systemctl"
fi
case "$home_dir" in
/*) ;;
*) echo "SENTINELA_HOME precisa ser um caminho absoluto: $home_dir" >&2; exit 2 ;;
esac
case "$home_dir$user_line" in
*'|'* | *'&'* | *'\'*) echo "SENTINELA_HOME e SENTINELA_USER não podem ter |, & ou \\" >&2; exit 2 ;;
esac
[ -n "$render" ] && dest="$render"

# unit_name: o nome gerado de um modelo (sentinela-worker.service -> <prefixo>-worker.service).
unit_name() { echo "$prefix-${1#sentinela-}"; }

# check_owner: recusa mexer numa unit que já existe e é de outra instalação.
check_owner() {
	f="$dest/$1"
	[ -f "$f" ] || return 0
	other="$(sed -n 's/^Environment=SENTINELA_HOME=//p' "$f" | head -n 1)"
	[ -z "$other" ] || [ "$other" = "$home_dir" ] && return 0
	echo "a unit $f é da instalação em $other, não desta ($home_dir)." >&2
	echo "nada foi alterado. Para uma segunda instalação, use outro nome: deploy/install.sh --prefix <nome> (ex.: --prefix ceres-sentinela)." >&2
	exit 3
}

for t in $TEMPLATES; do check_owner "$(unit_name "$t")"; done

if $uninstall; then
	$ctl disable --now $SERVICES || true
	for t in $TEMPLATES; do rm -f "$dest/$(unit_name "$t")"; done
	$ctl daemon-reload
	if $user_mode; then
		echo "units $prefix-* removidas (o linger continua; desligue com: loginctl disable-linger $USER)"
	else
		echo "units $prefix-* removidas"
	fi
	exit 0
fi

mkdir -p "$dest"
for t in $TEMPLATES; do
	u="$(unit_name "$t")"
	# Units geradas são arquivos, não links: o modelo do repo não tem caminho de máquina.
	rm -f "$dest/$u"
	if [ -n "$user_line" ]; then
		sed -e "s|@SENTINELA_HOME@|$home_dir|g" -e "s|@WANTED_BY@|$wanted|g" -e "s|^@USER_LINE@$|$user_line|" \
			-e "s|^SyslogIdentifier=sentinela-|SyslogIdentifier=$prefix-|" "$DIR/systemd/$t" >"$dest/$u"
	else
		sed -e "s|@SENTINELA_HOME@|$home_dir|g" -e "s|@WANTED_BY@|$wanted|g" -e "/^@USER_LINE@$/d" \
			-e "s|^SyslogIdentifier=sentinela-|SyslogIdentifier=$prefix-|" "$DIR/systemd/$t" >"$dest/$u"
	fi
done
if [ -n "$render" ]; then
	echo "units $prefix-* geradas em $dest (SENTINELA_HOME=$home_dir)"
	exit 0
fi

$ctl daemon-reload
$ctl enable --now $SERVICES
if $user_mode; then
	# Sem linger, os serviços do usuário só sobem depois do login; com ele, sobem no boot.
	loginctl enable-linger "$USER"
	systemctl --user list-timers "$prefix-scan.timer" --no-legend
else
	systemctl list-timers "$prefix-scan.timer" --no-legend
fi
