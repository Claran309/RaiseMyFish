# K0-7 离线检索开发评测

质量门禁：false；总体结构与质量通过：false。相关条目/全部实际返回条目 = 46/56 = 82.14%。正例覆盖 43/60；负例误召回 0/20；越界条目 0；合法镜像命中 20/20；逐条Context.Validate和Top3检查通过；预算 100/100。

阈值：相关率≥80%，正例覆盖≥48/60，负例误召回≤2/20，越界0，镜像控制20/20。无返回相关率记N/A；镜像/排序不进入质量分母。

| 正例形式 | 条数 | 命中预期 | 相关/返回 |
|---|---:|---:|---:|
| exact_title | 20 | 20 | 20/21 |
| natural_title | 20 | 20 | 22/24 |
| natural_paraphrase | 20 | 3 | 4/11 |
| alias | 0 | 0 | 0/0 |

生产Revision：`3c6e9e32d543f2bd62004a2f37910d1a72d8ff539883f00a4988e153298c4c43`；合成权限Revision：`6aff3275df0f9220115b9744f3eb4380a9e0baaa2157ebf0d9255020e2a94182`。

合成排序：预期 `sort-title, sort-a, sort-b`，实际 `sort-title, sort-a, sort-b`，重复10次，通过=true；只检查标题>关键词、priority及ID稳定排序，不代表自然查询质量。

预算：真实Brain + 显式Knowledge，固定合成人格，MaxPromptTokens=32768、10%；每条在内存fake Provider/Usage执行候选与空知识两次，独立比较最终消息字节增量与审计（含转义），静态system保持一致。未发送网络请求，fake服务端usage及费用均未知；没有真实模型或PG调用，不能代表真实35条人格开关对照或生产消费链路。

标注局限：开发助手预先按title/usage审读；非DSH独立标注；标题20、含标题自然句20、无目标标题语义场景20、alias0（500条keywords为空）。语义场景保留词面Reader当前不支持的需求，不把标题换皮称语义泛化。未列入预期的返回保守记不相关；未根据结果补标或更改Reader。全部私密资料明确合成，不含真实用户事实。

| ID | 类型/形式 | scope | 预期相关ID | 实际ID | 相关 | 命中/误召回/越界 | 控制ID | 知识字节/预算 |
|---|---|---|---|---|---:|---|---|---|
| pos-001 | positive/exact_title | live/eval-room/private=false | meme-custom-1649c19a5530d0729adc9e3d | meme-custom-1649c19a5530d0729adc9e3d | 1 | true/false/0 |  | 223/3276 |
| pos-002 | positive/exact_title | comment/eval-video/private=false | meme-custom-4d6e4fe7d384d4256f0a8a63 | meme-custom-4d6e4fe7d384d4256f0a8a63 | 1 | true/false/0 |  | 256/3276 |
| pos-003 | positive/exact_title | dm/eval-contact/private=true | meme-custom-8899e03c02cd65e70a106fbf | meme-custom-8899e03c02cd65e70a106fbf | 1 | true/false/0 |  | 223/3276 |
| pos-004 | positive/exact_title | qq/eval-contact/private=true | meme-custom-be185e2b73fc1b0dc9b5efe8 | meme-custom-be185e2b73fc1b0dc9b5efe8 | 1 | true/false/0 |  | 297/3276 |
| pos-005 | positive/exact_title | qq/eval-group/private=false | meme-custom-220ead26997603d65cd070f8 | meme-custom-220ead26997603d65cd070f8 | 1 | true/false/0 |  | 254/3276 |
| pos-006 | positive/exact_title | live/eval-room/private=false | meme-custom-472e3b757ae5442b980306c0 | meme-custom-472e3b757ae5442b980306c0 | 1 | true/false/0 |  | 241/3276 |
| pos-007 | positive/exact_title | comment/eval-video/private=false | meme-custom-43fee40ecc9fb94f46a419b7 | meme-custom-43fee40ecc9fb94f46a419b7 | 1 | true/false/0 |  | 461/3276 |
| pos-008 | positive/exact_title | dm/eval-contact/private=true | meme-custom-0b3bb52ecd9a94ddab39892d | meme-custom-0b3bb52ecd9a94ddab39892d | 1 | true/false/0 |  | 282/3276 |
| pos-009 | positive/exact_title | qq/eval-contact/private=true | meme-custom-42ff7d766df173e3e7be94b7 | meme-custom-42ff7d766df173e3e7be94b7, meme-custom-7b09cb4da7f39e1747cbd7aa | 1 | true/false/0 |  | 722/3276 |
| pos-010 | positive/exact_title | qq/eval-group/private=false | meme-custom-68c0224e491586f62b2fcbf8 | meme-custom-68c0224e491586f62b2fcbf8 | 1 | true/false/0 |  | 317/3276 |
| pos-011 | positive/exact_title | live/eval-room/private=false | meme-custom-27d7ce36e04083f8935fe7ef | meme-custom-27d7ce36e04083f8935fe7ef | 1 | true/false/0 |  | 247/3276 |
| pos-012 | positive/exact_title | comment/eval-video/private=false | meme-custom-15e2cbcbc71b812d199888da | meme-custom-15e2cbcbc71b812d199888da | 1 | true/false/0 |  | 256/3276 |
| pos-013 | positive/exact_title | dm/eval-contact/private=true | meme-custom-6d46eddcba0201fe3e28a005 | meme-custom-6d46eddcba0201fe3e28a005 | 1 | true/false/0 |  | 256/3276 |
| pos-014 | positive/exact_title | qq/eval-contact/private=true | meme-custom-afbc727e28d2e48f565840eb | meme-custom-afbc727e28d2e48f565840eb | 1 | true/false/0 |  | 235/3276 |
| pos-015 | positive/exact_title | qq/eval-group/private=false | meme-memes-0a524610d30ce232d650ad75 | meme-memes-0a524610d30ce232d650ad75 | 1 | true/false/0 |  | 565/3276 |
| pos-016 | positive/exact_title | live/eval-room/private=false | meme-memes-0edd74ced24163177b6171ec | meme-memes-0edd74ced24163177b6171ec | 1 | true/false/0 |  | 528/3276 |
| pos-017 | positive/exact_title | comment/eval-video/private=false | meme-memes-209185370e2743970accd09e | meme-memes-209185370e2743970accd09e | 1 | true/false/0 |  | 563/3276 |
| pos-018 | positive/exact_title | dm/eval-contact/private=true | meme-memes-1d4c582438decf1fdf143266 | meme-memes-1d4c582438decf1fdf143266 | 1 | true/false/0 |  | 684/3276 |
| pos-019 | positive/exact_title | qq/eval-contact/private=true | meme-memes-157caa5901bb85d72de2437e | meme-memes-157caa5901bb85d72de2437e | 1 | true/false/0 |  | 657/3276 |
| pos-020 | positive/exact_title | qq/eval-group/private=false | meme-memes-0e26cf1781cfcc4710a9d53a | meme-memes-0e26cf1781cfcc4710a9d53a | 1 | true/false/0 |  | 341/3276 |
| pos-021 | positive/natural_title | live/eval-room/private=false | meme-custom-280aa07ee8adca6bd528e571 | meme-custom-280aa07ee8adca6bd528e571 | 1 | true/false/0 |  | 242/3276 |
| pos-022 | positive/natural_title | comment/eval-video/private=false | meme-custom-9ea4993e414802a33f21eeaa | meme-custom-9ea4993e414802a33f21eeaa | 1 | true/false/0 |  | 230/3276 |
| pos-023 | positive/natural_title | dm/eval-contact/private=true | meme-custom-4435bd390bfab8a0f823df3f, meme-custom-fc173567c5a1e0f4885b6e88 | meme-custom-4435bd390bfab8a0f823df3f, meme-custom-fc173567c5a1e0f4885b6e88 | 2 | true/false/0 |  | 670/3276 |
| pos-024 | positive/natural_title | qq/eval-contact/private=true | meme-custom-197f7ea62e1ae8b1bcff036d | meme-custom-197f7ea62e1ae8b1bcff036d | 1 | true/false/0 |  | 398/3276 |
| pos-025 | positive/natural_title | qq/eval-group/private=false | meme-custom-1b4f855e102ed93a42f87272 | meme-custom-1b4f855e102ed93a42f87272 | 1 | true/false/0 |  | 223/3276 |
| pos-026 | positive/natural_title | live/eval-room/private=false | meme-custom-b9bd8e4500792b536bb58ef7 | meme-custom-b9bd8e4500792b536bb58ef7 | 1 | true/false/0 |  | 221/3276 |
| pos-027 | positive/natural_title | comment/eval-video/private=false | meme-custom-a84a761617fd35947afc34a5 | meme-custom-a84a761617fd35947afc34a5 | 1 | true/false/0 |  | 238/3276 |
| pos-028 | positive/natural_title | dm/eval-contact/private=true | meme-custom-53bce047f1b2d34189a97f29 | meme-custom-53bce047f1b2d34189a97f29 | 1 | true/false/0 |  | 234/3276 |
| pos-029 | positive/natural_title | qq/eval-contact/private=true | meme-custom-50af8cdd6932d38e1a40fad0, meme-custom-90b82f6dc982e6c8aca8452e | meme-custom-50af8cdd6932d38e1a40fad0, meme-custom-90b82f6dc982e6c8aca8452e | 2 | true/false/0 |  | 431/3276 |
| pos-030 | positive/natural_title | qq/eval-group/private=false | meme-custom-58457b8154ba4eeaa38a5032 | meme-custom-58457b8154ba4eeaa38a5032 | 1 | true/false/0 |  | 253/3276 |
| pos-031 | positive/natural_title | live/eval-room/private=false | meme-custom-1649c19a5530d0729adc9e3d | meme-custom-1649c19a5530d0729adc9e3d | 1 | true/false/0 |  | 223/3276 |
| pos-032 | positive/natural_title | comment/eval-video/private=false | meme-custom-715ed6a84b9210f424528464 | meme-custom-715ed6a84b9210f424528464 | 1 | true/false/0 |  | 223/3276 |
| pos-033 | positive/natural_title | dm/eval-contact/private=true | meme-custom-9219b8945065cb09a54796f6 | meme-custom-9219b8945065cb09a54796f6 | 1 | true/false/0 |  | 232/3276 |
| pos-034 | positive/natural_title | qq/eval-contact/private=true | meme-memes-0a98d7bfa46240ab7a3b5176 | meme-memes-0a98d7bfa46240ab7a3b5176, meme-memes-26387acb443ec0425f929632, meme-memes-49b02237d94aebdad53061af | 1 | true/false/0 |  | 1647/3276 |
| pos-035 | positive/natural_title | qq/eval-group/private=false | meme-memes-23316695fde331b1a16b0e8d | meme-memes-23316695fde331b1a16b0e8d | 1 | true/false/0 |  | 521/3276 |
| pos-036 | positive/natural_title | live/eval-room/private=false | meme-memes-0e26cf1781cfcc4710a9d53a | meme-memes-0e26cf1781cfcc4710a9d53a | 1 | true/false/0 |  | 341/3276 |
| pos-037 | positive/natural_title | comment/eval-video/private=false | meme-memes-0edd74ced24163177b6171ec | meme-memes-0edd74ced24163177b6171ec | 1 | true/false/0 |  | 528/3276 |
| pos-038 | positive/natural_title | dm/eval-contact/private=true | meme-memes-209185370e2743970accd09e | meme-memes-209185370e2743970accd09e | 1 | true/false/0 |  | 563/3276 |
| pos-039 | positive/natural_title | qq/eval-contact/private=true | meme-memes-03e047e8bbd07e266b6111fe | meme-memes-03e047e8bbd07e266b6111fe | 1 | true/false/0 |  | 455/3276 |
| pos-040 | positive/natural_title | qq/eval-group/private=false | meme-memes-0a524610d30ce232d650ad75 | meme-memes-0a524610d30ce232d650ad75 | 1 | true/false/0 |  | 565/3276 |
| pos-041 | positive/natural_paraphrase | live/eval-room/private=false | meme-custom-012fcce7187468b20bee37ca, meme-custom-0ead07c2c0991381f5f899bc, meme-custom-15e2cbcbc71b812d199888da, meme-custom-280aa07ee8adca6bd528e571, meme-custom-e0136ce72f4dff655188dac1 | meme-custom-15e2cbcbc71b812d199888da, meme-custom-280aa07ee8adca6bd528e571, meme-memes-ba191eb6ead7de25c0c586e8 | 2 | true/false/0 |  | 1051/3276 |
| pos-042 | positive/natural_paraphrase | comment/eval-video/private=false | meme-custom-1b4f855e102ed93a42f87272 |  | 0 | false/false/0 |  | 0/3276 |
| pos-043 | positive/natural_paraphrase | dm/eval-contact/private=true | meme-custom-4d6e4fe7d384d4256f0a8a63 |  | 0 | false/false/0 |  | 0/3276 |
| pos-044 | positive/natural_paraphrase | qq/eval-contact/private=true | meme-custom-42ff7d766df173e3e7be94b7, meme-custom-7b09cb4da7f39e1747cbd7aa |  | 0 | false/false/0 |  | 0/3276 |
| pos-045 | positive/natural_paraphrase | qq/eval-group/private=false | meme-custom-4e24def2a393bb964d2c328f |  | 0 | false/false/0 |  | 0/3276 |
| pos-046 | positive/natural_paraphrase | live/eval-room/private=false | meme-custom-be185e2b73fc1b0dc9b5efe8 | meme-memes-1f8eabc4c27a98d88ec79bca, meme-memes-a73021e1387fd5b93d6deb22, meme-memes-e8cf21a91b2757aea1333f8e | 0 | false/false/0 |  | 1539/3276 |
| pos-047 | positive/natural_paraphrase | comment/eval-video/private=false | meme-custom-68c0224e491586f62b2fcbf8 |  | 0 | false/false/0 |  | 0/3276 |
| pos-048 | positive/natural_paraphrase | dm/eval-contact/private=true | meme-custom-9ea4993e414802a33f21eeaa, meme-memes-04cd5e89e6f53de583698bc7 |  | 0 | false/false/0 |  | 0/3276 |
| pos-049 | positive/natural_paraphrase | qq/eval-contact/private=true | meme-custom-4435bd390bfab8a0f823df3f | meme-custom-4435bd390bfab8a0f823df3f | 1 | true/false/0 |  | 249/3276 |
| pos-050 | positive/natural_paraphrase | qq/eval-group/private=false | meme-memes-2b348c737aa81cd77b1a8af5 |  | 0 | false/false/0 |  | 0/3276 |
| pos-051 | positive/natural_paraphrase | live/eval-room/private=false | meme-memes-0c8cb86beea458b9eefe245d |  | 0 | false/false/0 |  | 0/3276 |
| pos-052 | positive/natural_paraphrase | comment/eval-video/private=false | meme-memes-0edd74ced24163177b6171ec |  | 0 | false/false/0 |  | 0/3276 |
| pos-053 | positive/natural_paraphrase | dm/eval-contact/private=true | meme-memes-0e26cf1781cfcc4710a9d53a |  | 0 | false/false/0 |  | 0/3276 |
| pos-054 | positive/natural_paraphrase | qq/eval-contact/private=true | meme-memes-04c392883ec199a734c4c746 | meme-memes-3f7c32f996032ecaa5850138, meme-memes-2ce7cd639a7377381f757fc1, meme-memes-60491bd0a442ddd08d16416a | 0 | false/false/0 |  | 1545/3276 |
| pos-055 | positive/natural_paraphrase | qq/eval-group/private=false | meme-memes-209185370e2743970accd09e |  | 0 | false/false/0 |  | 0/3276 |
| pos-056 | positive/natural_paraphrase | live/eval-room/private=false | meme-memes-0a524610d30ce232d650ad75 |  | 0 | false/false/0 |  | 0/3276 |
| pos-057 | positive/natural_paraphrase | comment/eval-video/private=false | meme-memes-03e047e8bbd07e266b6111fe |  | 0 | false/false/0 |  | 0/3276 |
| pos-058 | positive/natural_paraphrase | dm/eval-contact/private=true | meme-custom-50af8cdd6932d38e1a40fad0, meme-custom-90b82f6dc982e6c8aca8452e |  | 0 | false/false/0 |  | 0/3276 |
| pos-059 | positive/natural_paraphrase | qq/eval-contact/private=true | meme-custom-6d46eddcba0201fe3e28a005 | meme-custom-6d46eddcba0201fe3e28a005 | 1 | true/false/0 |  | 256/3276 |
| pos-060 | positive/natural_paraphrase | qq/eval-group/private=false | meme-custom-afbc727e28d2e48f565840eb |  | 0 | false/false/0 |  | 0/3276 |
| neg-001 | negative/natural | live/eval-room/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-002 | negative/natural | comment/eval-video/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-003 | negative/natural | dm/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-004 | negative/natural | qq/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-005 | negative/natural | qq/eval-group/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-006 | negative/natural | live/eval-room/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-007 | negative/natural | comment/eval-video/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-008 | negative/natural | dm/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-009 | negative/natural | qq/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-010 | negative/natural | qq/eval-group/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-011 | negative/natural | live/eval-room/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-012 | negative/natural | comment/eval-video/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-013 | negative/natural | dm/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-014 | negative/natural | qq/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-015 | negative/natural | qq/eval-group/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-016 | negative/natural | live/eval-room/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-017 | negative/natural | comment/eval-video/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-018 | negative/natural | dm/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-019 | negative/natural | qq/eval-contact/private=true |  |  | 0 | false/false/0 |  | 0/3276 |
| neg-020 | negative/natural | qq/eval-group/private=false |  |  | 0 | false/false/0 |  | 0/3276 |
| acl-001 | permission/synthetic | dm/synthetic-other/private=true |  |  | 0 | false/false/0 | synthetic-private-dm-a | 0/3276 |
| acl-002 | permission/synthetic | qq/synthetic-a/private=true |  |  | 0 | false/false/0 | synthetic-private-dm-a | 0/3276 |
| acl-003 | permission/synthetic | live/eval-room/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-a | 0/3276 |
| acl-004 | permission/synthetic | comment/eval-video/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-a | 0/3276 |
| acl-005 | permission/synthetic | qq/eval-group/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-a | 0/3276 |
| acl-006 | permission/synthetic | dm/synthetic-other/private=true |  |  | 0 | false/false/0 | synthetic-private-dm-b | 0/3276 |
| acl-007 | permission/synthetic | qq/synthetic-b/private=true |  |  | 0 | false/false/0 | synthetic-private-dm-b | 0/3276 |
| acl-008 | permission/synthetic | live/eval-room/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-b | 0/3276 |
| acl-009 | permission/synthetic | comment/eval-video/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-b | 0/3276 |
| acl-010 | permission/synthetic | qq/eval-group/private=false |  |  | 0 | false/false/0 | synthetic-private-dm-b | 0/3276 |
| acl-011 | permission/synthetic | qq/synthetic-other/private=true |  |  | 0 | false/false/0 | synthetic-private-qq-a | 0/3276 |
| acl-012 | permission/synthetic | dm/synthetic-a/private=true |  |  | 0 | false/false/0 | synthetic-private-qq-a | 0/3276 |
| acl-013 | permission/synthetic | live/eval-room/private=false |  |  | 0 | false/false/0 | synthetic-private-qq-a | 0/3276 |
| acl-014 | permission/synthetic | comment/eval-video/private=false |  |  | 0 | false/false/0 | synthetic-private-qq-a | 0/3276 |
| acl-015 | permission/synthetic | qq/eval-group/private=false |  |  | 0 | false/false/0 | synthetic-private-qq-a | 0/3276 |
| acl-016 | permission/synthetic | live/eval-room/private=false |  |  | 0 | false/false/0 | synthetic-scene-qq-private | 0/3276 |
| acl-017 | permission/synthetic | comment/eval-video/private=false |  |  | 0 | false/false/0 | synthetic-scene-qq-private | 0/3276 |
| acl-018 | permission/synthetic | dm/eval-contact/private=true |  |  | 0 | false/false/0 | synthetic-scene-qq-private | 0/3276 |
| acl-019 | permission/synthetic | qq/eval-group/private=false |  |  | 0 | false/false/0 | synthetic-scene-qq-private | 0/3276 |
| acl-020 | permission/synthetic | qq/another-group/private=false |  |  | 0 | false/false/0 | synthetic-scene-qq-private | 0/3276 |

仅为开发侧离线子集交付。K0-7正式35条开关对照、真实PG验收、DSH独立复核及扩量均未执行；质量不足时停止，不改冻结题或Reader追分。
