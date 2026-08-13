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
# Pooling is a property of the MODEL, not a preference: EmbeddingGemma is
# mean-pooled, while decoder-based embedders (Qwen3-Embedding and friends) are
# pooled on their last token and need POOLING=last. Getting this wrong does not
# error — it silently produces vectors that rank almost at random.
POOLING="${POOLING:-mean}"
# Logical batch (-b): the token budget for one request. The indexer sends
# semindex.DefaultBatch (64) messages at a time, and if their combined tokens
# exceed BATCH the server rejects the request and the indexer retries them ONE
# AT A TIME — correct, but far slower. Keep it generous.
BATCH="${BATCH:-8192}"
# Physical micro-batch (-ub): how many tokens go to the GPU in a single
# dispatch. This is the knob for DESKTOP SMOOTHNESS, not throughput. One
# 8192-token dispatch is a long-running kernel that the compositor cannot
# preempt, which is what a full backfill looks like as system-wide frame drops;
# smaller dispatches give the GPU scheduler gaps to draw your screen in. Lower
# it if indexing makes the desktop stutter, raise it toward BATCH for maximum
# indexing speed on a headless box.
UBATCH="${UBATCH:-1024}"
# Per-input context. llama.cpp clamps this to the model's trained context, so
# EmbeddingGemma runs at 2048 whatever is asked for here (/props confirms it).
# Left roomy so a longer-context embedder doesn't need a change.
CTX="${CTX:-8192}"

if [ ! -f "$MODEL" ]; then
	echo "embedding model not found: $MODEL" >&2
	echo "download an embedding GGUF and set MODEL=... (see comments in this script)" >&2
	exit 1
fi

# --embeddings flips the server into embedding mode (exposes /v1/embeddings).
# --pooling defaults to mean, which matches EmbeddingGemma's sentence-embedding
#   training (it mean-pools then applies two dense projections). Do NOT force
#   --pooling cls. Override with POOLING=last for a decoder-based embedder.
# (No --embd-normalize: that's a llama-embedding CLI flag, not a llama-server
#  one. matterbox's embed client L2-normalizes every vector itself.)
# -fa 1 (flash attention) keeps the attention buffers small; leave it on.
#
# A word on memory: these numbers are cheap for a 300M encoder — this server
# sits at ~0.4 GB resident — but they are NOT model-independent. The same flags
# on Qwen3-Embedding-0.6B (a 28-layer, 1024-hidden decoder) produced an 8.4 GB
# process from a 640 MB model, because the compute buffers scale with
# UBATCH × layers × hidden. If you point this at a bigger embedder and memory
# gets tight, UBATCH is the dial to turn first — it bounds the compute buffers
# and, unlike BATCH, lowering it does not push the indexer into its slow
# one-message-at-a-time path.
#
# NGL is how many layers run on the GPU. -ngl 0 keeps the model entirely on the
# CPU: slower per message, but it frees the GPU completely, which is the one
# setting that makes a long backfill invisible to the desktop. Worth trying on a
# 300M encoder before accepting a stuttering screen for five hours.
NGL="${NGL:-99}"
exec env VK_DRIVER_FILES="$MESA_ICD" "$LLAMA" \
	-m "$MODEL" \
	--embeddings \
	--pooling "$POOLING" \
	-ngl "$NGL" \
	-fa 1 \
	-c "$CTX" \
	-b "$BATCH" \
	-ub "$UBATCH" \
	--mlock \
	--host "$HOST" \
	--port "$PORT"
