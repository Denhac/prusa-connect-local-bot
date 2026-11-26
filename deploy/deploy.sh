#!/bin/sh
# Put a release of the module where a prusa-connect server offers it, and read
# what the module logged.
#
#   deploy/deploy.sh push HOST [RELEASE_DIR]   copy a release (default: the newest in bin/release)
#   deploy/deploy.sh status HOST               the module's recent lines in the server's journal
#
# HOST is what ssh takes, such as rpi@printserver.
#
# push copies the release into the directory the server's core.modules_local
# names (README.md, "Deploying"). The running module is not touched: an admin
# then chooses Install or Update on the server's Extensions page. The release
# that was there before is kept beside it as <dir>.previous.
#
# Environment:
#   SSHPASS           the login password, when the host has no SSH key for you (needs sshpass)
#   PI_SUDO_PASSWORD  the sudo password on the host; SSHPASS when unset
#   SSH_KNOWN_HOSTS   a known_hosts file to check the host key against (it is always checked)
#   MODULES_LOCAL_DIR the directory core.modules_local names
#                     (default /var/lib/prusa-connect/modules-local/slack-bot)
#   SERVER_USER       the user the server runs as (default prusa-connect)
#   SERVER_UNIT       the server's systemd unit (default prusa-connect)
set -eu

DEST=${MODULES_LOCAL_DIR:-/var/lib/prusa-connect/modules-local/slack-bot}
OWNER=${SERVER_USER:-prusa-connect}
UNIT=${SERVER_UNIT:-prusa-connect}

fail() { echo "deploy.sh: $*" >&2; exit 1; }

usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

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

status() {
	host=$1
	remote "$host" "journalctl -u '$UNIT' --since '-30 min' --no-pager -o short-iso 2>/dev/null |
		grep -E 'slack-bot|sink:slack-bot|module=slack-bot' | tail -n 60" ||
		echo "no lines from the module in the last 30 minutes (or the journal is not readable as this user)"
}

[ $# -ge 2 ] || usage
cd "$(dirname "$0")/.."

case $1 in
push) shift; push "$@" ;;
status) shift; status "$1" ;;
*) usage ;;
esac
