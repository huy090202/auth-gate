#!/usr/bin/env bash
# Create the IAM user auth-gate-deployer with infra/deployer-policy.json, the permissions
# boundary auth-gate-boundary from infra/role-boundary.json, and a local AWS profile of the
# same name, which the other scripts use by default. Rerun it after changing either JSON.
# Run with an admin profile: ADMIN_PROFILE=default ./00-deployer.sh
# The secret access key goes straight into ~/.aws/credentials; it is never printed.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v jq >/dev/null || die "jq is required"

USER_NAME=auth-gate-deployer
BOUNDARY_NAME=auth-gate-boundary
admin=(--profile "${ADMIN_PROFILE:-default}")

if aws iam get-user "${admin[@]}" --user-name "$USER_NAME" >/dev/null 2>&1; then
  echo "user $USER_NAME exists; updating its policy only"
else
  aws iam create-user "${admin[@]}" --user-name "$USER_NAME" >/dev/null
  echo "created user $USER_NAME"
fi

account="$(aws sts get-caller-identity "${admin[@]}" --query Account --output text)"

# The ceiling on every auth-gate-* role. It comes before the deployer policy, which only lets
# the deployer make roles under this ARN, and before the early exit on an existing access key.
boundary_arn="arn:aws:iam::$account:policy/$BOUNDARY_NAME"
boundary_file="$ROOT/infra/role-boundary.json"
if default_version="$(aws iam get-policy "${admin[@]}" --policy-arn "$boundary_arn" \
  --query Policy.DefaultVersionId --output text 2>/dev/null)"; then
  current="$(aws iam get-policy-version "${admin[@]}" --policy-arn "$boundary_arn" --version-id "$default_version" \
    --query PolicyVersion.Document --output json | jq -Sc .)"
  if [ "$current" = "$(jq -Sc . "$boundary_file")" ]; then
    echo "boundary $BOUNDARY_NAME unchanged ($default_version)"
  else
    # IAM keeps at most 5 versions, and never lets the default one go.
    count="$(aws iam list-policy-versions "${admin[@]}" --policy-arn "$boundary_arn" \
      --query 'length(Versions)' --output text)"
    if [ "$count" -ge 5 ]; then
      oldest="$(aws iam list-policy-versions "${admin[@]}" --policy-arn "$boundary_arn" \
        --query 'sort_by(Versions[?!IsDefaultVersion], &CreateDate)[0].VersionId' --output text)"
      aws iam delete-policy-version "${admin[@]}" --policy-arn "$boundary_arn" --version-id "$oldest"
      echo "removed boundary version $oldest"
    fi
    new_version="$(aws iam create-policy-version "${admin[@]}" --policy-arn "$boundary_arn" \
      --policy-document "file://$boundary_file" --set-as-default --query PolicyVersion.VersionId --output text)"
    echo "boundary $BOUNDARY_NAME updated to $new_version (was $default_version)"
  fi
else
  aws iam create-policy "${admin[@]}" --policy-name "$BOUNDARY_NAME" \
    --policy-document "file://$boundary_file" >/dev/null
  echo "created boundary $BOUNDARY_NAME"
fi

# A managed policy, not inline: inline user policies are capped at 2 KB.
policy_arn="arn:aws:iam::$account:policy/$USER_NAME-policy"
doc="file://$ROOT/infra/deployer-policy.json"
if aws iam get-policy "${admin[@]}" --policy-arn "$policy_arn" >/dev/null 2>&1; then
  # IAM keeps at most 5 versions; drop the oldest non-default one before adding a new one.
  oldest="$(aws iam list-policy-versions "${admin[@]}" --policy-arn "$policy_arn" \
    --query 'reverse(Versions[?!IsDefaultVersion])[0].VersionId' --output text)"
  if [ "$oldest" != "None" ]; then
    aws iam delete-policy-version "${admin[@]}" --policy-arn "$policy_arn" --version-id "$oldest"
  fi
  aws iam create-policy-version "${admin[@]}" --policy-arn "$policy_arn" \
    --policy-document "$doc" --set-as-default >/dev/null
  echo "updated policy $USER_NAME-policy"
else
  aws iam create-policy "${admin[@]}" --policy-name "$USER_NAME-policy" --policy-document "$doc" >/dev/null
  echo "created policy $USER_NAME-policy"
fi
aws iam attach-user-policy "${admin[@]}" --user-name "$USER_NAME" --policy-arn "$policy_arn"

if aws configure get aws_access_key_id --profile "$USER_NAME" >/dev/null 2>&1; then
  echo "profile $USER_NAME already configured; not creating another access key"
  exit 0
fi

# IAM returns the secret only once, so everything that can fail first goes before create-access-key.
# lib.sh and this script set no other EXIT trap, so this one overwrites nothing.
csv="$(mktemp)"
trap 'rm -f "$csv"' EXIT
aws configure set region "$REGION" --profile "$USER_NAME"
aws configure set output json --profile "$USER_NAME"

read -r key_id secret < <(aws iam create-access-key "${admin[@]}" --user-name "$USER_NAME" \
  --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
# Imported from a file because aws configure set would put the secret on argv. printf is a builtin,
# mktemp makes the file 0600, and import names the profile after the User Name column.
if ! { printf 'User Name,Access key ID,Secret access key\n%s,%s,%s\n' "$USER_NAME" "$key_id" "$secret" >"$csv" \
  && aws configure import --csv "file://$csv" >/dev/null; }; then
  die "access key $key_id exists in IAM but its secret was not saved and is lost. Delete the key, then rerun this script: aws iam delete-access-key ${admin[*]} --user-name $USER_NAME --access-key-id $key_id"
fi
echo "configured profile $USER_NAME (key $key_id)"
