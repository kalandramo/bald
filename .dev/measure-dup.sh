#!/bin/sh
# dup-clone：两树归一化后「重复行」计数（同时在 k/r 两侧出现的归一化行）。
cd /d/code/bald || exit 2
total=0
for f in server.go logger.go options.go kind.go contract/contract.go contract/provider.go; do
  [ -f "transport/kafka/$f" ] && [ -f "transport/rabbitmq/$f" ] || continue
  norm() { sed 's|//.*||' "$1" | sed 's/[[:space:]]//g' | sed 's/[kK]afka/brokerx/g; s/[rR]abbitmq/brokerx/g; s/RABBITMQ/BROKERX/g; s/KAFKA/BROKERX/g'; }
  norm "transport/kafka/$f" | sort > /tmp/mc-k.txt
  norm "transport/rabbitmq/$f" | sort > /tmp/mc-r.txt
  d=$(comm -12 /tmp/mc-k.txt /tmp/mc-r.txt | wc -l)
  echo "pair $f: $d" >&2
  total=$((total + d))
done
echo "$total"
