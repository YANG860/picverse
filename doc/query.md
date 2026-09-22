proto消息规范

fail ,err字段规范
一般批量查询，需要标识部分成功，故使用fail,err字段标识。
单条查询接口，原子性的批量接口，不需要使用fail,err字段标识部分错误的内容，直接使用grpc的原生错误机制传递。



原则：

在任何情况下，不承诺保证返回的顺序，必须使用result的Key信息进行定位


query格式

// 暂无强制要求
message query{
    int64 id = 1;
    string str =2; //可以复合查询
}


result格式

message result{
    // 错误信息，必须
    bool fail =1;
    common.v1.Error err=2;

    // 查询的Key信息，可以多个key，直接复制查询的类型和字段，保证字段一致可追溯回查询，必须
    queryType query =3; //repeated场景下
    //或者
    int64 id =3; //map场景下

    // 对应这组key的返回数据
    dataType data =4;

}

1-key对1-val查询：使用map

map<key,result>

多key，复合key，1-key对n-val：使用repeated

repeated result
