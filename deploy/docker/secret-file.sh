#!/bin/sh

runnerhub_load_secret() {
  env_name="$1"
  file_env_name="$2"

  eval "current=\${${env_name}:-}"
  [ -n "$current" ] && return 0

  eval "secret_file=\${${file_env_name}:-}"
  [ -n "$secret_file" ] || return 0

  if [ ! -r "$secret_file" ]; then
    echo "RunnerHub cannot read $env_name from $secret_file" >&2
    return 1
  fi

  secret_value="$(cat "$secret_file")"
  if [ -z "$secret_value" ]; then
    echo "RunnerHub secret file is empty: $secret_file" >&2
    return 1
  fi

  export "$env_name=$secret_value"
}
