#!/usr/bin/env sh
# Start a local llama.cpp server for matterbox's SEMANTIC SEARCH embeddings.
#
# This is a SECOND server, separate from the chat/summary one on :8321 — an
# embedding model has to be loaded with --embeddings, so it can't share the
# instance that serves the Gemma chat model. matterbox reaches it via
# config.yaml -> embeddings.endpoint (default http://127.0.0.1:8322).
#
# Launch it yourself (matterbox never auto-starts a model server). Override any
# path/port with an env var, e.g.:  MODEL=/path/to/model.gguf PORT=8400 ./llama-embeddings.sh
#
# Model: EmbeddingGemma-300M, official ggml-org QAT GGUF (ungated; QAT means the
# Q8 quant ≈ full precision — important, as EmbeddingGemma's activations don't
# tolerate plain f16). Multilingual (NL+EN), Matryoshka so config.yaml `dim` can
# shrink vectors. Downloaded with:
#   wget -O ~/Models/embeddinggemma-300m-qat-Q8_0.gguf \
#     https://huggingface.co/ggml-org/embeddinggemma-300m-qat-q8_0-GGUF/resolve/main/embeddinggemma-300m-qat-Q8_0.gguf

set -eu

# Optimized Asahi/Vulkan mesa driver (same one the chat server uses).
MESA_ICD="${MESA_ICD:-/home/corne/Source/mesa/build/src/asahi/vulkan/asahi_devenv_icd.aarch64.json}"
LLAMA="${LLAMA:-/home/corne/Source/llama.cpp/build-vulkan/bin/llama-server}"
MODEL="${MODEL:-/home/corne/Models/embeddinggemma-300m-qat-Q8_0.gguf}"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8322}"

if [ ! -f "$MODEL" ]; then
	echo "embedding model not found: $MODEL" >&2
	echo "download an embedding GGUF and set MODEL=... (see comments in this script)" >&2
	exit 1
fi

# --embeddings flips the server into embedding mode (exposes /v1/embeddings).
# --pooling mean matches EmbeddingGemma's sentence-embedding training (it mean-
#   pools then applies two dense projections). Do NOT force --pooling cls.
# (No --embd-normalize: that's a llama-embedding CLI flag, not a llama-server
#  one. matterbox's embed client L2-normalizes every vector itself.)
# Large -b/-ub let matterbox's backfill send a whole batch of messages in one
#   request without splitting. -c is the per-input context (EmbeddingGemma: 2048,
#   but a roomy batch budget is cheap on a 300M model).
exec env VK_DRIVER_FILES="$MESA_ICD" "$LLAMA" \
	-m "$MODEL" \
	--embeddings \
	--pooling mean \
	-ngl 99 \
	-fa 1 \
	-c 8192 \
	-b 8192 \
	-ub 8192 \
	--mlock \
	--host "$HOST" \
	--port "$PORT"
