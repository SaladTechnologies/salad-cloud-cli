@{
    IncludeDefaultRules = $True
    Rules = @{
        PSAvoidExclaimOperator = @{
            Enable = $True
        }
        PSAvoidLongLines = @{
            Enable = $True
            MaximumLineLength = 120
        }
        PSAvoidUsingCmdletAliases = @{}
        PSAvoidSemicolonsAsLineTerminators = @{
            Enable = $True
        }
        PSAvoidUsingDoubleQuotesForConstantString = @{
            Enable = $True
        }
        PSPlaceCloseBrace = @{
            Enable = $True
            NoEmptyLineBefore = $True
            IgnoreOneLineBlock = $False
        }
        PSPlaceOpenBrace = @{
            Enable = $True
            IgnoreOneLineBlock = $False
        }
        PSUseConsistentIndentation = @{
            Enable = $True
        }
        PSUseConsistentWhitespace = @{
            Enable = $True
            CheckPipeForRedundantWhitespace = $True
            CheckParameter = $True
        }
        PSUseCorrectCasing = @{
            Enable = $True
        }
    }
}
