#!/usr/bin/env sh
# Start a local llama.cpp server for matterbox's CHAT / INFERENCE.
#
# This is a standard (non-embedding) server. matterbox reaches it via
# config.yaml -> model.endpoint (default http://127.0.0.1:8323).
#
# Launch it yourself (matterbox never auto-starts a model server). Override any
# path/port with an env var, e.g.:  MODEL=/path/to/model.gguf PORT=8400 ./llama-chat.sh
#
# Model: Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf
#   A Mixture-of-Experts model: 35B total params, ~3B active per token.
#   Q4_K_XL quantization gives a good speed/quality trade-off on Apple Silicon.
#   Supports 128K context; this script defaults to 32K as a practical server
#   balance (enough for long documents + RAG without exhausting RAM).
#   Place at ~/Models/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf or override MODEL=...

set -eu

# Optimized Asahi/Vulkan mesa driver (same one the embedding server uses).
MESA_ICD="${MESA_ICD:-/home/corne/Source/mesa/build/src/asahi/vulkan/asahi_devenv_icd.aarch64.json}"
LLAMA="${LLAMA:-/home/corne/Source/llama.cpp/build-vulkan/bin/llama-server}"
MODEL="${MODEL:-/home/corne/Models/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf}"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8323}"

if [ ! -f "$MODEL" ]; then
	echo "chat model not found: $MODEL" >&2
	echo "download a chat GGUF and set MODEL=... (see comments in this script)" >&2
	exit 1
fi

# -ngl 99    = offload all layers to GPU (essential for a 35B-class model).
# -fa 1      = flash attention (saves KV-cache memory and speeds up long contexts).
# -c 32768   = max context length. Qwen3 supports 128K, but 32K is a practical
#              default for a server — it keeps the 22GB model + KV cache within
#              typical Apple Silicon unified-memory budgets.
# -b 4096    = batch size. Conservative default for a 35B model; raise if you
#              have plenty of RAM and want higher throughput.
# -ub 4096   = micro-batch size (same reasoning as -b).
# --mlock    = lock pages into RAM so the OS doesn't swap the model out.
# -np 1      = one concurrent slot. Raise if you want to handle multiple chat
#              sessions in parallel (each extra slot costs KV-cache memory).
exec env VK_DRIVER_FILES="$MESA_ICD" "$LLAMA" \
	-m "$MODEL" \
	-ngl 99 \
	-fa 1 \
	-c 32768 \
	-b 4096 \
	-ub 4096 \
	--mlock \
	-np 1 \
	--host "$HOST" \
	--port "$PORT"
