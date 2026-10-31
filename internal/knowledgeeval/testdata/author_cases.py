"""一次性编写开发侧标注，不导入Reader、不读取评测输出；重放只按公开标题取ID。"""
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[3]
raw = (ROOT / "personas/dafeiyu/knowledge/memes.md").read_text(encoding="utf-8")
items = {}
for match in re.finditer(r'^  - id: (.+)\n    title: (.+)\n', raw, re.M):
    items[json.loads(match[2])] = json.loads(match[1])

rows = []
scopes = [dict(Channel="live", Session="eval-room", Private=False), dict(Channel="comment", Session="eval-video", Private=False), dict(Channel="dm", Session="eval-contact", Private=True), dict(Channel="qq", Session="eval-contact", Private=True), dict(Channel="qq", Session="eval-group", Private=False)]

def positive(form, query, titles, label, reason):
    rows.append(dict(id=f"pos-{len(rows)+1:03}", kind="positive", form=form, query=query, scope=scopes[len(rows)%5], expected_ids=[items[t] for t in titles], labels=[label], rationale=reason))

exact = ["术式公开", "无量空处", "伏魔御厨子", "救救我魔虚罗大人", "何时眼前突兀见此屋", "颇具浪漫主义气质啊", "松针大油边", "垫完你的垫你的", "攒怒气值", "我的麦炸吗", "善意对比", "出必还愿", "我想要那四个字", "失踪人口回归", "科隆展摆完挂机", "霜华漫天", "多巴胺穿搭", "乌萨斯倒霉熊", "击云连接大脑，化龙代替思考", "运输大队长"]
for title in exact:
    positive("exact_title", title, [title], "明确词条查询", "直接询问已审读原词及接法，期望同名词条。")

with_title = [
 ("这次十连不歪，终于抽到想要的了。", ["不歪"], "抽卡", "接法明确用于抽到目标。"),
 ("刚看到童年那部动画的片头，爷青回！", ["爷青回"], "怀旧", "接法用于青春回忆。"),
 ("这期视频又是10分，评分也太大方了吧。", ["又是10分", "又是"], "评分", "调侃满分泛滥；通用又是句式也相关。"),
 ("修了半天才说要重启，我去不早说。", ["我去不早说"], "马后炮", "接法针对显而易见的提醒。"),
 ("我两个套餐都舍不得放弃，我全都要。", ["我全都要"], "选择", "接法为全选。"),
 ("大佬白送这么详细的教程，赛博菩萨啊。", ["赛博菩萨"], "慷慨救场", "接法用于夸慷慨或救场。"),
 ("这波配合都不用开麦，意念合一了。", ["意念合一"], "配合", "接法夸默契。"),
 ("这结局看到我泪目，太戳人了。", ["泪目"], "感动", "接法为被感动的短反应。"),
 ("三分钟讲不完吗，课代表快来给我总结。", ["课代表", "省流"], "总结", "求别人总结及一句总结的接法相关。"),
 ("你这操作是开挂吗，求同款键盘。", ["同款键盘"], "操作夸赞", "把厉害操作归因设备是反向夸人。"),
 ("说要亮底牌，那就术式公开吧。", ["术式公开"], "讲原理", "接法明确用于讲原理亮底牌。"),
 ("别吵啦，我不想搭理这种多余服务，大可不必。", ["大可不必"], "婉拒", "礼貌拒绝多余操作。"),
 ("这人下班还在回工作消息，班味好重。", ["班味"], "职场疲惫", "词条描绘上班疲惫。"),
 ("你们都说它不好看，可爱捏，我就爱这个小猫。", ["可爱捏"], "宠物", "软萌夸赞动物。"),
 ("能在一秒里换三套衣服，这就是F1换装吧。", ["F1换装"], "快速换装", "接法形容快速换衣。"),
 ("这对手不断给我们送装备，运输大队长辛苦了。", ["运输大队长"], "送资源", "用于对手持续送资源。"),
 ("你这段话全堆华丽词藻，又开始霜华漫天了。", ["霜华漫天"], "故弄玄虚", "接法为堆词不说人话。"),
 ("刚刷到一身亮粉亮黄色的衣服，多巴胺穿搭真鲜艳。", ["多巴胺穿搭"], "穿搭", "接法指色彩鲜艳令人愉快的穿搭。"),
 ("终于有强力辅助带弱角色飞，谁还有梦想啊。", ["谁还有梦想"], "配队", "接法正是辅助抬升弱角色。"),
 ("厂商参展只有展台和周边，科隆展摆完挂机，情报呢？", ["科隆展摆完挂机"], "展会", "接法为参展不发实质情报。"),
]
for query,titles,label,reason in with_title:
    positive("natural_title",query,titles,label,reason)

paraphrases = [
 ("抽卡前想讨个好运，保佑今天出货！", ["锦鲤", "阿弥陀佛", "出必还愿", "不歪", "会赢的"], "抽卡祈愿", "祈祷、许愿与抽到目标接法均可接此场景。"),
 ("红色和蓝色都喜欢，这两个能不能一起买？", ["我全都要"], "全选", "语义为不愿二选一。"),
 ("这一长串解释把脑子塞满了，现在完全看懵。", ["无量空处"], "信息过载", "接法为信息过载、脑子宕机。"),
 ("他连续被坑了三次还在忍着没发作。", ["攒怒气值", "怒气值"], "忍耐积怒", "两条接法均指压抑怒气逐渐积累。"),
 ("今天轮到我值日，还排着一个地狱难度的需求。", ["下一话就到我打宿傩了"], "轮到上场", "接法明确提及值日和地狱需求。"),
 ("这个bug实在修不动了，得喊大佬来救一下。", ["救救我魔虚罗大人"], "求援", "接法提到修bug找大佬。"),
 ("直播声音突然爆音，电流声听着好刺耳。", ["我的麦炸吗"], "音频故障", "接法针对爆音、电流音。"),
 ("一看到童年动画的片头曲就想起放学回家的日子。", ["爷青回", "三根皮带"], "青春回忆", "前者泛怀旧，后者用于灌篮高手OP等旧动漫空耳。"),
 ("每次看到满分评测都觉得这个评分越来越不值钱。", ["又是10分"], "评分泛滥", "接法调侃满分泛滥。"),
 ("他嘴上一直吹自己多强，真让他动手就怂了。", ["口嗨"], "只说不做", "接法为说大话而不行动。"),
 ("我最喜欢的博主突然被曝出负面新闻，滤镜碎了。", ["塌房"], "形象崩塌", "接法为公众人物负面新闻导致粉丝幻灭。"),
 ("这篇文章堆了好多华丽辞藻，完全看不懂想说什么。", ["霜华漫天"], "堆砌辞藻", "接法指故弄玄虚的文艺表达。"),
 ("对面一整局持续送装备和资源，真得谢谢他们。", ["运输大队长"], "送资源", "接法明确调侃对手不断送装备资源。"),
 ("视频开场快速切镜头配音乐，一下就把人抓住了。", ["开幕惊艳"], "开场剪辑", "接法为节奏快速且抓眼的开场。"),
 ("今天想穿得五颜六色一点，看着心里也开心。", ["多巴胺穿搭"], "鲜艳衣着", "接法为鲜艳缤纷的愉悦穿搭。"),
 ("官方来展会只有卖周边的摊位，新PV一条都没有。", ["科隆展摆完挂机"], "无实质情报", "接法为展会只卖周边不发PV。"),
 ("弱角色搭上超级辅助后，强度竟然飞起来了。", ["谁还有梦想"], "辅助抬强度", "接法为强力辅助抬升弱势角色。"),
 ("长视频没空看完，有没有人用一句话讲重点？", ["课代表", "省流"], "总结请求", "两个接法分别为求总结和自行一句总结。"),
 ("生日到了，有没有人送我一句祝福？", ["我想要那四个字"], "生日求祝福", "接法为生日求祝福的暗号。"),
 ("刚看到一个半年没更新的UP突然发了新视频。", ["失踪人口回归"], "长期停更回归", "接法为长期不更新UP回来。"),
]
for query,titles,label,reason in paraphrases:
    positive("natural_paraphrase",query,titles,label,reason)

negatives = [
 "请问二次函数的判别式怎么推导？", "明天北京会不会下雨？", "能帮我查一下这趟高铁几点到站吗？", "PostgreSQL的事务隔离级别分别是什么？", "我想预约下周二上午的牙科门诊。", "这个月电费账单在哪里缴？", "番茄炒鸡蛋先炒哪一种？", "三角形三边已知如何求面积？", "这份合同的签署日期是哪一天？", "帮我把会议安排到周五下午两点。", "请解释一下光合作用的化学方程式。", "我的包裹现在到哪个快递站了？", "护照到期换发需要哪些材料？", "请计算一下17乘以23。", "这台洗衣机的排水管直径是多少？", "茶叶应该存放在什么温度？", "从市图书馆到火车站怎么坐车？", "如何在Go里读取一个本地文件？", "牛顿第二定律的适用条件是什么？", "请把这句话翻译成法语：早上好。"
]
for i,query in enumerate(negatives,1):
    rows.append(dict(id=f"neg-{i:03}",kind="negative",form="natural",query=query,scope=scopes[(i-1)%5],expected_ids=[],labels=["无库内可用接法"],rationale="当前公开梗库不提供此项事实服务或知识解答。"))

private_fixtures = [
 ("synthetic-private-dm-a", "测试琥珀备忘", dict(Channel="dm",Session="synthetic-a",Private=True)),
 ("synthetic-private-dm-b", "测试青瓷备忘", dict(Channel="dm",Session="synthetic-b",Private=True)),
 ("synthetic-private-qq-a", "测试玄武备忘", dict(Channel="qq",Session="synthetic-a",Private=True)),
]
for j,(fid,phrase,origin) in enumerate(private_fixtures):
    targets=[dict(Channel=origin["Channel"],Session="synthetic-other",Private=True),dict(Channel="qq" if origin["Channel"]=="dm" else "dm",Session=origin["Session"],Private=True),scopes[0],scopes[1],scopes[4]]
    for i,target in enumerate(targets):
        rows.append(dict(id=f"acl-{j*5+i+1:03}",kind="permission",form="synthetic",query=f"请告诉我{phrase}的内容。",scope=target,expected_ids=[],blocked_ids=[fid],control_scope=origin,labels=["合成私密",["跨会话","跨平台同名会话","公开直播","公开评论","QQ群"][i]],rationale="明确非真实私密；同输入仅合法源scope可召回。"))
for i,target in enumerate([scopes[0],scopes[1],scopes[2],scopes[4],dict(Channel="qq",Session="another-group",Private=False)],16):
    rows.append(dict(id=f"acl-{i:03}",kind="permission",form="synthetic",query="请讲讲测试紫檀频道说明。",scope=target,expected_ids=[],blocked_ids=["synthetic-scene-qq-private"],control_scope=scopes[3],labels=["合成场景限制","仅qq_private"],rationale="public资料仍必须先按scene过滤；QQ私聊镜像应命中。"))

assert len(rows)==100
out=pathlib.Path(__file__).with_name("cases.jsonl")
if out.exists():
    raise SystemExit("cases已存在，拒绝重写冻结题")
out.write_text("".join(json.dumps(row,ensure_ascii=False,separators=(",",":"))+"\n" for row in rows),encoding="utf-8",newline="\n")

def entry(fid,title,scenes,private=False,scope=None,keywords=None,priority=50,tags=None):
    fields=[f"  - id: {json.dumps(fid)}", f"    title: {json.dumps(title,ensure_ascii=False)}", f"    channel: {json.dumps(scenes)}", f"    keywords: {json.dumps(keywords or [],ensure_ascii=False)}", f"    tags: {json.dumps(tags or [],ensure_ascii=False)}",f"    priority: {priority}", f"    visibility: {'private' if private else 'public'}", f"    source: synthetic:{fid}", "    summary: 合成资料，无真实用户信息。", "    usage: 仅用于离线权限或排序验证。"]
    if scope:
        fields += ["    scope:",f"      channel: {scope['Channel']}",f"      session: {scope['Session']}","      private: true"]
    return "\n".join(fields)+"\n"

acl="---\nschema: fish-knowledge/v1\nentries:\n"
for fid,title,scope in private_fixtures:
    acl+=entry(fid,title,["dm","qq_private","live","comment","qq_group"],True,scope)
acl+=entry("synthetic-scene-qq-private","测试紫檀频道说明",["qq_private"])
order="---\nschema: fish-knowledge/v1\nentries:\n"
order+=entry("sort-title","排序碰撞测试",["live"],priority=1)
for fid,priority in [("sort-b",50),("sort-c",20),("sort-a",50)]:
    order+=entry(fid,"独立"+fid,["live"],keywords=["排序碰撞测试"],priority=priority)
order+=entry("sort-tag","独立标签",["live"],tags=["排序碰撞测试"],priority=100)
for name,body in [("batch-synthetic-acl.md",acl),("batch-synthetic-order.md",order)]:
    target=pathlib.Path(__file__).with_name(name)
    if target.exists(): raise SystemExit("fixture已存在，拒绝重写")
    target.write_text(body+"---\n",encoding="utf-8",newline="\n")
