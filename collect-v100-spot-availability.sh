#!/usr/bin/env bash
set -Eeuo pipefail

# AWS Spot A10G availability probe. The AWS zone name is ap-northeast-2c.
AWS_REGION="${AWS_REGION:-ap-northeast-2}"
AWS_AZ="${AWS_AZ:-ap-northeast-2c}"
INSTANCE_TYPE="${INSTANCE_TYPE:-g5.xlarge}"
REQUESTS_PER_PROBE="${REQUESTS_PER_PROBE:-1}"
INTERVAL_SECONDS="${INTERVAL_SECONDS:-300}"
LAUNCH_TIMEOUT_SECONDS="${LAUNCH_TIMEOUT_SECONDS:-300}"
DURATION_SECONDS="${DURATION_SECONDS:-1209600}" # 14 days
RUN_ONCE="${RUN_ONCE:-0}"
OUTPUT_DIR="${OUTPUT_DIR:-$HOME/a10g-spot-availability}"
LOG_RETENTION_COUNT="${LOG_RETENTION_COUNT:-10}"
AMI_ID="${AMI_ID:-}"
SUBNET_ID="${SUBNET_ID:-}"
SECURITY_GROUP_ID="${SECURITY_GROUP_ID:-}"

if ! command -v aws >/dev/null 2>&1; then
  echo "AWS CLI is required." >&2
  exit 1
fi
if ! aws sts get-caller-identity --region "$AWS_REGION" >/dev/null 2>&1; then
  echo "AWS credentials are unavailable or invalid. Configure AWS CLI first." >&2
  exit 1
fi
if ! [[ "$REQUESTS_PER_PROBE" =~ ^[1-9][0-9]*$ && "$INTERVAL_SECONDS" =~ ^[0-9]+$ && "$DURATION_SECONDS" =~ ^[0-9]+$ ]]; then
  echo "REQUESTS_PER_PROBE, INTERVAL_SECONDS, and DURATION_SECONDS must be non-negative integers; REQUESTS_PER_PROBE must be positive." >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR/logs"

safe_name() {
  printf '%s' "$1" | tr -c '[:alnum:]' '_'
}

prune_old_logs() {
  local keep_count="$LOG_RETENTION_COUNT"
  if [[ "$keep_count" =~ ^[0-9]+$ ]] && (( keep_count >= 0 )); then
    find "$OUTPUT_DIR/logs" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' \
      | sort -nr | awk '{print $2}' | tail -n +$((keep_count + 1)) | xargs -r rm -rf
  fi
}

discover_network() {
  local vpc_id
  vpc_id="$(aws ec2 describe-vpcs --region "$AWS_REGION" \
    --filters Name=isDefault,Values=true Name=state,Values=available \
    --query 'Vpcs[0].VpcId' --output text)"
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || {
    echo "No default VPC found. Set SUBNET_ID and SECURITY_GROUP_ID." >&2
    exit 1
  }

  if [[ -z "$SUBNET_ID" ]]; then
    SUBNET_ID="$(aws ec2 describe-subnets --region "$AWS_REGION" \
      --filters "Name=vpc-id,Values=$vpc_id" "Name=availability-zone,Values=$AWS_AZ" Name=state,Values=available \
      --query 'Subnets[0].SubnetId' --output text)"
  fi
  if [[ -z "$SECURITY_GROUP_ID" ]]; then
    SECURITY_GROUP_ID="$(aws ec2 describe-security-groups --region "$AWS_REGION" \
      --filters "Name=vpc-id,Values=$vpc_id" Name=group-name,Values=default \
      --query 'SecurityGroups[0].GroupId' --output text)"
  fi
  [[ -n "$SUBNET_ID" && "$SUBNET_ID" != "None" ]] || { echo "No available subnet in $AWS_AZ." >&2; exit 1; }
  [[ -n "$SECURITY_GROUP_ID" && "$SECURITY_GROUP_ID" != "None" ]] || { echo "No default security group found." >&2; exit 1; }
}

discover_ami() {
  if [[ -z "$AMI_ID" ]]; then
    AMI_ID="$(aws ssm get-parameter --region "$AWS_REGION" \
      --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
      --query 'Parameter.Value' --output text)"
  fi
  [[ -n "$AMI_ID" && "$AMI_ID" != "None" ]] || { echo "Unable to resolve AMI_ID." >&2; exit 1; }
}

validate_offering() {
  local offering
  offering="$(aws ec2 describe-instance-type-offerings --region "$AWS_REGION" \
    --location-type availability-zone --filters Name=instance-type,Values="$INSTANCE_TYPE" \
    --query "InstanceTypeOfferings[?Location=='$AWS_AZ'].Location" --output text)"
  if [[ -z "$offering" ]]; then
    echo "$INSTANCE_TYPE is not offered in $AWS_AZ. Choose an AWS-supported A10G type/AZ before probing." >&2
    exit 1
  fi
}

write_header() {
  local csv="$1"
  if [[ ! -f "$csv" ]]; then
    printf 'date,time_utc,timestamp_utc,region,availability_zone,instance_type,requested,received,failed,probe_result,failure_reason,duration_seconds\n' > "$csv"
  fi
}

launch_one() {
  local index="$1" probe_dir="$2" name instance_id start end duration result reason
  name="a10g-probe-$(safe_name "$AWS_AZ")-$(date -u +%Y%m%dT%H%M%S)-$index"
  start="$(date +%s)"
  set +e
  instance_id="$(aws ec2 run-instances --region "$AWS_REGION" --image-id "$AMI_ID" \
    --instance-type "$INSTANCE_TYPE" --count 1 --subnet-id "$SUBNET_ID" \
    --security-group-ids "$SECURITY_GROUP_ID" --no-associate-public-ip-address \
    --instance-market-options 'MarketType=spot,SpotOptions={SpotInstanceType=one-time,InstanceInterruptionBehavior=terminate}' \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$name},{Key=Purpose,Value=a10g-spot-availability-probe}]" \
    --query 'Instances[0].InstanceId' --output text 2>"$probe_dir/request-$index.err")"
  if [[ $? -ne 0 || -z "$instance_id" || "$instance_id" == "None" ]]; then
    result="failed"
    reason="$(tr '\n' ' ' < "$probe_dir/request-$index.err" | sed 's/,/;/g')"
    printf 'FAIL,%s,%s\n' "$reason" "$name" > "$probe_dir/result-$index"
    set -e
    return 1
  fi

  if timeout "$LAUNCH_TIMEOUT_SECONDS" aws ec2 wait instance-running --region "$AWS_REGION" --instance-ids "$instance_id" >"$probe_dir/wait-$index.log" 2>&1; then
    result="received"
    reason="none"
  else
    result="failed"
    reason="capacity_or_launch_timeout"
  fi
  end="$(date +%s)"
  duration=$((end - start))
  printf '%s,%s,%s,%s\n' "$result" "$reason" "$instance_id" "$duration" > "$probe_dir/result-$index"
  aws ec2 terminate-instances --region "$AWS_REGION" --instance-ids "$instance_id" >/dev/null 2>&1 || true
  aws ec2 cancel-spot-instance-requests --region "$AWS_REGION" \
    --spot-instance-request-ids "$(aws ec2 describe-spot-instance-requests --region "$AWS_REGION" \
      --filters "Name=instance-id,Values=$instance_id" --query 'SpotInstanceRequests[].SpotInstanceRequestId' --output text)" >/dev/null 2>&1 || true
  set -e
  [[ "$result" == "received" ]]
}

probe() {
  local csv="$OUTPUT_DIR/$(safe_name "$AWS_AZ").csv" probe_start timestamp date_utc time_utc probe_dir start end duration received failed result reason index
  write_header "$csv"
  probe_start="$(date +%s)"
  while [[ "$RUN_ONCE" == "1" || $(( $(date +%s) - probe_start )) -lt "$DURATION_SECONDS" ]]; do
    timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
    date_utc="$(date -u +%Y-%m-%d)"
    time_utc="$(date -u +%H:%M:%S)"
    probe_dir="$OUTPUT_DIR/logs/$(safe_name "$AWS_AZ")_$timestamp"
    mkdir -p "$probe_dir"
    start="$(date +%s)"
    received=0
    failed=0
    for ((index = 1; index <= REQUESTS_PER_PROBE; index++)); do
      launch_one "$index" "$probe_dir" >"$probe_dir/launch-$index.log" 2>&1 &
    done
    wait || true
    for ((index = 1; index <= REQUESTS_PER_PROBE; index++)); do
      if [[ -f "$probe_dir/result-$index" ]] && head -n 1 "$probe_dir/result-$index" | grep -q '^received,'; then
        received=$((received + 1))
      else
        failed=$((failed + 1))
      fi
    done
    end="$(date +%s)"
    duration=$((end - start))
    if (( received == REQUESTS_PER_PROBE )); then result="all_received"; else result="$([[ $received -gt 0 ]] && echo partial || echo none_received)"; fi
    if grep -RqiE 'InsufficientInstanceCapacity|capacity' "$probe_dir"; then reason="capacity_unavailable"
    elif grep -RqiE 'MaxSpotInstanceCountExceeded|VcpuLimitExceeded|InsufficientInstanceCapacity' "$probe_dir"; then reason="quota_or_capacity_error"
    elif (( failed > 0 )); then reason="other_error"; else reason="none"; fi
    printf '%s,%s,%s,%s,%s,%s,%d,%d,%d,%s,%s,%d\n' "$date_utc" "$time_utc" "$timestamp" "$AWS_REGION" "$AWS_AZ" "$INSTANCE_TYPE" "$REQUESTS_PER_PROBE" "$received" "$failed" "$result" "$reason" "$duration" >> "$csv"
    prune_old_logs
    [[ "$RUN_ONCE" == "1" ]] && break
    sleep "$INTERVAL_SECONDS"
  done
}

validate_offering
discover_network
discover_ami
echo "AWS A10G Spot probe: region=$AWS_REGION az=$AWS_AZ type=$INSTANCE_TYPE requests=$REQUESTS_PER_PROBE"
echo "AMI=$AMI_ID subnet=$SUBNET_ID security_group=$SECURITY_GROUP_ID"
probe