#!/bin/sh
# Put a release of the module where a prusa-connect server offers it, and read
# what the module logged.
#
#   deploy/deploy.sh push HOST [RELEASE_DIR]   copy a release (default: the newest in bin/release)
#   deploy/deploy.sh status HOST               the module's recent lines in the server's journal
#   deploy/deploy.sh backup HOST [DIR]         make a server backup on HOST and copy it into DIR (default .)
#
# HOST is what ssh takes, such as rpi@printserver.
#
# push copies the release into the directory the server's core.modules_local
# names (README.md, "Deploying"). The running module is not touched: an admin
# then chooses Install or Update on the server's Extensions page. The release
# that was there before is kept beside it as <dir>.previous.
#
# backup runs the server's own `prusa-connect backup create`, a consistent
# snapshot of its configuration, its databases (the accounts among them) and
# every module's data directory, this bot's state file and audit log included.
# It then copies that backup off the Pi as DIR/prusa-connect-<backup id>.tar.gz,
# readable only by you: it holds password hashes. docs/operations.md says how
# to restore one.
#
# Environment:
#   SSHPASS           the login password, when the host has no SSH key for you (needs sshpass)
#   PI_SUDO_PASSWORD  the sudo password on the host; SSHPASS when unset
#   SSH_KNOWN_HOSTS   a known_hosts file to check the host key against (it is always checked)
#   MODULES_LOCAL_DIR the directory core.modules_local names
#                     (default /var/lib/prusa-connect/modules-local/slack-bot)
#   SERVER_USER       the user the server runs as (default prusa-connect)
#   SERVER_UNIT       the server's systemd unit (default prusa-connect)
#   SERVER_HOME       the server's working directory (default /var/lib/prusa-connect)
#   SERVER_BIN        the server's binary (default $SERVER_HOME/bin/prusa-connect)
#   SERVER_CONFIG     its configuration file (default /etc/prusa-connect/config.yaml)
#   BACKUP_ROOT       where it keeps backups (default $SERVER_HOME/config/config.d/upgrade/backups)
set -eu

DEST=${MODULES_LOCAL_DIR:-/var/lib/prusa-connect/modules-local/slack-bot}
OWNER=${SERVER_USER:-prusa-connect}
UNIT=${SERVER_UNIT:-prusa-connect}
HOME_DIR=${SERVER_HOME:-/var/lib/prusa-connect}
BIN=${SERVER_BIN:-$HOME_DIR/bin/prusa-connect}
CONFIG=${SERVER_CONFIG:-/etc/prusa-connect/config.yaml}
BACKUPS=${BACKUP_ROOT:-$HOME_DIR/config/config.d/upgrade/backups}

fail() { echo "deploy.sh: $*" >&2; exit 1; }

usage() { sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

# remote runs a command on HOST, checking the host key strictly.
remote() {
	host=$1; shift
	set -- ssh -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o LogLevel=ERROR \
		${SSH_KNOWN_HOSTS:+-o UserKnownHostsFile="$SSH_KNOWN_HOSTS"} "$host" "$@"
	if [ -n "${SSHPASS:-}" ]; then
		command -v sshpass >/dev/null || fail "SSHPASS is set and sshpass is not installed"
		sshpass -e "$@"
	else
		"$@"
	fi
}

# newest is the release directory with the highest version under bin/release.
newest() {
	dir=$(find bin/release -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort -V | tail -n 1)
	[ -n "$dir" ] || fail "no release under bin/release. Run make release first"
	echo "$dir"
}

push() {
	host=$1
	release=${2:-$(newest)}
	[ -f "$release/module.yaml" ] && [ -f "$release/checksums.txt" ] || fail "$release is not a release directory"
	( cd "$release" && sha256sum --quiet -c checksums.txt ) || fail "$release does not match its checksums.txt"
	version=$(sed -n 's/^version: *//p' "$release/module.yaml")

	echo "copying $release ($version) to $host:$DEST"

	# The first line on standard input is the sudo password, which the remote
	# shell reads before the archive that follows. It never lands in a file
	# or on a command line.
	{
		printf '%s\n' "${PI_SUDO_PASSWORD:-${SSHPASS:-}}"
		tar -C "$release" -czf - .
	} | remote "$host" "
		set -e
		IFS= read -r pw
		printf '%s\n' \"\$pw\" | sudo -S -p '' -v 2>/dev/null || sudo -n -v
		sudo -n sh -c '
			set -e
			mkdir -p \"\$(dirname \"$DEST\")\"
			tmp=\$(mktemp -d \"$DEST.new.XXXXXX\")
			tar -xzf - -C \"\$tmp\"
			chown -R \"$OWNER:$OWNER\" \"\$tmp\"
			chmod 755 \"\$tmp\"
			if [ -d \"$DEST\" ]; then rm -rf \"$DEST.previous\"; mv \"$DEST\" \"$DEST.previous\"; fi
			mv \"\$tmp\" \"$DEST\"
			echo \"in place: \$(sed -n \"s/^version: *//p\" \"$DEST/module.yaml\")\"
		'
	"

	echo "Now open the server's Extensions page and choose Install or Update for the Slack bot."
}

# sudoed runs a shell command as root on HOST. The sudo password goes on the
# first line of standard input, never on a command line.
sudoed() {
	host=$1; script=$2
	printf '%s\n' "${PI_SUDO_PASSWORD:-${SSHPASS:-}}" | remote "$host" "
		set -e
		IFS= read -r pw
		printf '%s\n' \"\$pw\" | sudo -S -p '' -v 2>/dev/null || sudo -n -v
		$script
	"
}

backup() {
	host=$1; out=$2
	[ -d "$out" ] || fail "$out is not a directory"

	echo "making a server backup on $host (this copies its databases and module releases: some seconds)"
	answer=$(sudoed "$host" "sudo -n -u '$OWNER' sh -c \"cd '$HOME_DIR' && exec '$BIN' backup create -config '$CONFIG' -json\"") ||
		fail "the server could not make a backup"
	# The manifest's first "id" is the backup's; its files carry names, not ids.
	id=$(printf '%s' "$answer" | grep -o '"id": *"[^"/]*"' | head -n 1 | sed 's/.*: *"\([^"]*\)"/\1/')
	[ -n "$id" ] || fail "the server's answer named no backup"

	file="$out/prusa-connect-$id.tar.gz"
	umask 077
	sudoed "$host" "sudo -n tar -C '$BACKUPS' -czf - '$id'" >"$file.part" || {
		rm -f "$file.part"
		fail "could not copy $id from $host"
	}
	mv "$file.part" "$file"

	echo "saved $file ($(du -h "$file" | cut -f1)), readable only by you: it holds password hashes."
	echo "The server keeps $id on the Pi too, with the last few. docs/operations.md says how to restore."
}

status() {
	host=$1
	remote "$host" "journalctl -u '$UNIT' --since '-30 min' --no-pager -o short-iso 2>/dev/null |
		grep -E 'slack-bot|sink:slack-bot|module=slack-bot' | tail -n 60" ||
		echo "no lines from the module in the last 30 minutes (or the journal is not readable as this user)"
}

[ $# -ge 2 ] || usage

# A backup's DIR is where you are, not the repository.
out=$(cd "${3:-.}" 2>/dev/null && pwd) || out=${3:-.}
cd "$(dirname "$0")/.."

case $1 in
push) shift; push "$@" ;;
status) shift; status "$1" ;;
backup) backup "$2" "$out" ;;
*) usage ;;
esac
