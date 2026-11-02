#!/usr/bin/env python3
"""Генерирует КРОШЕЧНЫЕ GGUF-модели со случайными весами для проверки «сантехники»
(загрузка, эмбеддинги, чат, стриминг) без скачивания настоящих моделей.
Качество ответов у них нулевое — это только smoke-тест.

  pip install gguf numpy
  python3 scripts/make_test_models.py out_dir
"""
import sys, os
import numpy as np
import gguf

def make(path, n_embd, n_layer, pooling=None, seed=0):
    rng = np.random.default_rng(seed)
    n_head, n_ff, ctx = n_embd // 32, n_embd * 2, 2048
    tokens, scores, types = [], [], []
    for t in ["<unk>", "<s>", "</s>"]:
        tokens.append(t); scores.append(0.0)
        types.append(2 if t == "<unk>" else 3)
    for b in range(256):
        tokens.append("<0x%02X>" % b); scores.append(0.0); types.append(6)
    tokens.append("\u2581"); scores.append(-1.0); types.append(1)
    n_vocab = len(tokens)
    w = gguf.GGUFWriter(path, "llama")
    w.add_name("tiny-test")
    w.add_context_length(ctx); w.add_embedding_length(n_embd); w.add_block_count(n_layer)
    w.add_feed_forward_length(n_ff); w.add_head_count(n_head); w.add_head_count_kv(n_head)
    w.add_rope_dimension_count(n_embd // n_head); w.add_layer_norm_rms_eps(1e-5)
    w.add_vocab_size(n_vocab)
    w.add_tokenizer_model("llama"); w.add_token_list(tokens); w.add_token_scores(scores); w.add_token_types(types)
    w.add_bos_token_id(1); w.add_eos_token_id(2); w.add_unk_token_id(0)
    w.add_add_bos_token(True); w.add_add_eos_token(pooling is not None)
    if pooling is not None:
        w.add_pooling_type(gguf.PoolingType.MEAN)
    w.add_chat_template("{% for m in messages %}{{ '<|im_start|>' + m['role'] + '\n' + m['content'] + '<|im_end|>\n' }}{% endfor %}{% if add_generation_prompt %}{{ '<|im_start|>assistant\n' }}{% endif %}")
    def t(name, *shape, scale=0.05):
        w.add_tensor(name, (rng.standard_normal(shape) * scale).astype(np.float32))
    def ones(name, n): w.add_tensor(name, np.ones(n, dtype=np.float32))
    t("token_embd.weight", n_vocab, n_embd); ones("output_norm.weight", n_embd); t("output.weight", n_vocab, n_embd)
    for i in range(n_layer):
        ones(f"blk.{i}.attn_norm.weight", n_embd); ones(f"blk.{i}.ffn_norm.weight", n_embd)
        for n in ("q", "k", "v"): t(f"blk.{i}.attn_{n}.weight", n_embd, n_embd)
        t(f"blk.{i}.attn_output.weight", n_embd, n_embd)
        t(f"blk.{i}.ffn_gate.weight", n_ff, n_embd); t(f"blk.{i}.ffn_up.weight", n_ff, n_embd); t(f"blk.{i}.ffn_down.weight", n_embd, n_ff)
    w.write_header_to_file(); w.write_kv_data_to_file(); w.write_tensors_to_file(); w.close()

if __name__ == "__main__":
    out = sys.argv[1] if len(sys.argv) > 1 else "testmodels"
    os.makedirs(out, exist_ok=True)
    make(os.path.join(out, "tiny-chat-test.gguf"), 64, 2)
    make(os.path.join(out, "tiny-embed-test.gguf"), 64, 2, pooling=True, seed=1)
    print("готово:", out)
