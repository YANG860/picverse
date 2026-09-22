package testsnowflake

import "rednote/packages/utils"

func InitTestSnoflake(id int64) {
	err := utils.InitSnowflake(id)
	if err != nil {
		panic(err)
	}
}
