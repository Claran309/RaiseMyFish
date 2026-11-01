-- 只写来源字典，不导入历史余额、私信或运行时事实。
INSERT INTO memory_sources(id,label,weight) VALUES
('owner_direct','主人直接对话',100),
('owner_interaction','主人互动信号',80),
('persona','人格与风格',60),
('expression','表达素材',50),
('content','内容消费与产出',30),
('reading','深度阅读',20)
ON CONFLICT(id) DO NOTHING;
