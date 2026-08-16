set -eu
upgrade=0
[ -e "/definitely/not/here" ] && upgrade=1
echo "reached end upgrade=$upgrade"
