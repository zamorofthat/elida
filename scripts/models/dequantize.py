#!/usr/bin/env python3
# Adopted verbatim from the 2026-10-04 pure-Go feasibility spike, which
# validated its output: full_check passes, 38 matmul chains and 3 embedding
# tables rewritten, 748 -> 606 nodes, 23.0 MB -> 90.4 MB, and a maximum
# calibrated-probability delta of 1.1e-6 against the int8 source through
# Hugot's backend. Change it only with a fresh parity run.
"""Rewrite an ORT dynamically-quantized ONNX graph back to pure float32.

Handles the two int8 patterns emitted by onnxruntime.quantization.quantize_dynamic:

  1. Weighted matmuls:
       DynamicQuantizeLinear(X) -> Xq, x_scale, x_zp
       MatMulInteger(Xq, Wq, x_zp, w_zp) -> int32
       Cast(int32) -> float
       Mul(x_scale * w_scale) [scales pre-multiplied by a helper Mul]
     becomes
       MatMul(X, W_fp32)   with W_fp32 = (Wq - w_zp) * w_scale

  2. Quantized embedding tables:
       Gather(Wq_uint8, ids) -> DequantizeLinear(scale, zp)
     becomes
       Gather(W_fp32, ids)

Usage: dequantize.py <in_model.onnx> <out_dir>
"""
import collections
import os
import shutil
import sys

import numpy as np
import onnx
from onnx import TensorProto, helper, numpy_helper


def build_maps(graph):
    producer = {}
    for n in graph.node:
        for o in n.output:
            producer[o] = n
    consumers = collections.defaultdict(list)
    for n in graph.node:
        for i in n.input:
            if i:
                consumers[i].append(n)
    return producer, consumers


def dequant_weight(wq, w_zp, w_scale):
    """(Wq - zp) * scale with per-tensor or per-channel (last-axis) scale."""
    w = wq.astype(np.float32)
    zp = np.asarray(w_zp, dtype=np.float32)
    sc = np.asarray(w_scale, dtype=np.float32)
    if zp.ndim == 0 and sc.ndim == 0:
        return (w - zp) * sc
    # per-channel: ORT quantizes weights stored [K, N] along axis 1
    n_out = w.shape[-1]
    if sc.size == n_out:
        shape = [1] * (w.ndim - 1) + [n_out]
    elif sc.size == 1:
        shape = [1] * w.ndim
    else:
        raise ValueError(f"scale size {sc.size} matches no axis of weight {w.shape}")
    zp_b = zp.reshape(shape) if zp.size == sc.size else zp.reshape([1] * w.ndim)
    return (w - zp_b) * sc.reshape(shape)


def main():
    src = sys.argv[1] if len(sys.argv) > 1 else "model/model.onnx"
    out_dir = sys.argv[2] if len(sys.argv) > 2 else "model-fp32-defender"
    src_dir = os.path.dirname(os.path.abspath(src))

    model = onnx.load(src)
    g = model.graph
    before_nodes = collections.Counter(n.op_type for n in g.node)
    before_total = len(g.node)
    before_inits = len(g.initializer)

    inits = {i.name: i for i in g.initializer}
    producer, consumers = build_maps(g)

    drop_nodes = set()        # id() of NodeProto to remove
    drop_inits = set()        # initializer names to remove
    new_nodes = []            # (anchor_node_id, NodeProto) inserted in place
    new_inits = []

    def arr(name):
        return numpy_helper.to_array(inits[name])

    # ---- pattern 1: MatMulInteger chains -------------------------------
    mmi_rewritten = 0
    for n in g.node:
        if n.op_type != "MatMulInteger":
            continue
        xq, wq_name, x_zp, w_zp_name = n.input[0], n.input[1], n.input[2], n.input[3]
        if wq_name not in inits or w_zp_name not in inits:
            raise RuntimeError(f"{n.name}: weight/zero-point not an initializer")

        dql = producer.get(xq)
        if dql is None or dql.op_type != "DynamicQuantizeLinear":
            raise RuntimeError(f"{n.name}: input[0] not from DynamicQuantizeLinear "
                               f"(got {dql.op_type if dql else 'graph input'})")
        x_real = dql.input[0]

        cast_cons = consumers[n.output[0]]
        if len(cast_cons) != 1 or cast_cons[0].op_type != "Cast":
            raise RuntimeError(f"{n.name}: expected single Cast consumer, got "
                               f"{[c.op_type for c in cast_cons]}")
        cast = cast_cons[0]
        mul_cons = consumers[cast.output[0]]
        if len(mul_cons) != 1 or mul_cons[0].op_type != "Mul":
            raise RuntimeError(f"{n.name}: expected single Mul after Cast, got "
                               f"{[c.op_type for c in mul_cons]}")
        mul = mul_cons[0]

        other = [i for i in mul.input if i != cast.output[0]]
        if len(other) != 1:
            raise RuntimeError(f"{n.name}: Mul inputs unexpected {list(mul.input)}")
        scales_mul = producer.get(other[0])
        if scales_mul is None or scales_mul.op_type != "Mul":
            raise RuntimeError(f"{n.name}: scale operand not produced by a Mul "
                               f"(got {scales_mul.op_type if scales_mul else 'init'})")
        w_scale_names = [i for i in scales_mul.input if i in inits]
        if len(w_scale_names) != 1:
            raise RuntimeError(f"{n.name}: scales Mul has {len(w_scale_names)} initializer inputs")
        w_scale_name = w_scale_names[0]

        w_fp32 = dequant_weight(arr(wq_name), arr(w_zp_name), arr(w_scale_name))
        new_name = wq_name.replace("_quantized", "") + "_fp32"
        new_inits.append(numpy_helper.from_array(w_fp32.astype(np.float32), new_name))
        new_nodes.append((id(n), helper.make_node(
            "MatMul", [x_real, new_name], [mul.output[0]],
            name=(n.name or wq_name) + "_dequant")))

        drop_nodes.update({id(n), id(cast), id(mul), id(scales_mul)})
        drop_inits.update({wq_name, w_zp_name, w_scale_name})
        mmi_rewritten += 1

    # ---- pattern 2: quantized embedding Gather -> DequantizeLinear -----
    emb_rewritten = 0
    for n in g.node:
        if n.op_type != "DequantizeLinear":
            continue
        gather = producer.get(n.input[0])
        if gather is None or gather.op_type != "Gather":
            raise RuntimeError(f"{n.name}: DequantizeLinear input not from Gather "
                               f"(got {gather.op_type if gather else 'init'})")
        table_name = gather.input[0]
        if table_name not in inits:
            raise RuntimeError(f"{n.name}: gather table {table_name} is not an initializer")
        scale_name, zp_name = n.input[1], n.input[2]
        table = dequant_weight(arr(table_name), arr(zp_name), arr(scale_name))
        new_name = table_name.replace("_quantized", "") + "_fp32"
        new_inits.append(numpy_helper.from_array(table.astype(np.float32), new_name))
        # rewire the Gather to emit the dequantized name directly
        gather.input[0] = new_name
        gather.output[0] = n.output[0]
        drop_nodes.add(id(n))
        drop_inits.update({table_name, scale_name, zp_name})
        emb_rewritten += 1

    # ---- drop now-orphaned DynamicQuantizeLinear nodes ------------------
    surviving = [n for n in g.node if id(n) not in drop_nodes]
    live_inputs = set()
    for n in surviving:
        live_inputs.update(n.input)
    for _, n in new_nodes:
        live_inputs.update(n.input)
    for n in surviving:
        if n.op_type == "DynamicQuantizeLinear" and not any(o in live_inputs for o in n.output):
            drop_nodes.add(id(n))

    # ---- rebuild node list, inserting MatMuls where their chain was ----
    insert_at = {anchor: node for anchor, node in new_nodes}
    rebuilt = []
    for n in g.node:
        if id(n) in insert_at:
            rebuilt.append(insert_at[id(n)])
        if id(n) not in drop_nodes:
            rebuilt.append(n)

    # ---- iteratively prune dead nodes ----------------------------------
    graph_outputs = {o.name for o in g.output}
    while True:
        needed = set(graph_outputs)
        for n in rebuilt:
            needed.update(i for i in n.input if i)
        pruned = [n for n in rebuilt
                  if any(o in needed for o in n.output if o)]
        if len(pruned) == len(rebuilt):
            break
        rebuilt = pruned

    del g.node[:]
    g.node.extend(rebuilt)

    # ---- initializers: drop quantized ones, add fp32, drop unreferenced -
    referenced = set()
    for n in g.node:
        referenced.update(i for i in n.input if i)
    kept = [i for i in g.initializer
            if i.name not in drop_inits and i.name in referenced]
    del g.initializer[:]
    g.initializer.extend(kept)
    g.initializer.extend(ni for ni in new_inits if ni.name in referenced)

    # ---- validate -------------------------------------------------------
    onnx.checker.check_model(model, full_check=True)
    leftover = [n.op_type for n in g.node if n.op_type in
                ("MatMulInteger", "DynamicQuantizeLinear", "QuantizeLinear", "DequantizeLinear")]
    if leftover:
        raise RuntimeError(f"quantized ops survived: {collections.Counter(leftover)}")
    for i in g.initializer:
        if i.data_type in (TensorProto.INT8, TensorProto.UINT8):
            raise RuntimeError(f"int8/uint8 initializer survived: {i.name}")

    os.makedirs(out_dir, exist_ok=True)
    out_path = os.path.join(out_dir, "model.onnx")
    onnx.save(model, out_path)
    for fn in ("tokenizer.json", "tokenizer_config.json", "config.json",
               "special_tokens_map.json", "vocab.txt"):
        s = os.path.join(src_dir, fn)
        if os.path.exists(s):
            shutil.copy2(s, os.path.join(out_dir, fn))

    after = collections.Counter(n.op_type for n in g.node)
    print(f"rewrote {mmi_rewritten} MatMulInteger chains, {emb_rewritten} embedding tables")
    print(f"nodes        {before_total} -> {len(g.node)}")
    print(f"initializers {before_inits} -> {len(g.initializer)}")
    print(f"size         {os.path.getsize(src)/1e6:.1f} MB -> {os.path.getsize(out_path)/1e6:.1f} MB")
    print("\nop type before -> after:")
    for op in sorted(set(before_nodes) | set(after)):
        b, a = before_nodes.get(op, 0), after.get(op, 0)
        flag = "  <<<" if b != a else ""
        print(f"  {op:26s} {b:4d} -> {a:4d}{flag}")
    print(f"\nwrote {out_path}")


if __name__ == "__main__":
    main()
