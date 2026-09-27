package com.redxai.core

data class RXDiagnostic(val line:Int,val message:String,val error:Boolean)
data class RXValidationResult(val diagnostics:List<RXDiagnostic>) { val valid:Boolean get()=diagnostics.none{it.error} }

object RedXAIValidator {
    const val MAX_BYTES=2*1024*1024
    fun validate(source:String):RXValidationResult {
        if(source.toByteArray().size>MAX_BYTES)return RXValidationResult(listOf(RXDiagnostic(1,"Document exceeds 2 MiB.",true)))
        val lines=source.lines()
        if(lines.none{it.contains("{Red-XAI}[1]{")})return RXValidationResult(listOf(RXDiagnostic(1,"Missing required Red-XAI root.",true)))
        val d=mutableListOf<RXDiagnostic>(); var curly=0; var square=0; var quoted=false; var escaped=false
        lines.forEachIndexed{i,line->
            line.forEach{ch->
                if(escaped){escaped=false;return@forEach}
                if(ch=='\\'&&quoted){escaped=true;return@forEach}
                if(ch=='"'){quoted=!quoted;return@forEach}
                if(!quoted){when(ch){'{'=>curly++;'}'->curly--;'['->square++;']'->square--}}
            }}
            val t=line.trim(); if(t.contains("=")&&t.contains("{")&&!t.endsWith(","))d+=RXDiagnostic(i+1,"Packer assignments must end with a comma.",true)
        }
        if(quoted)d+=RXDiagnostic(lines.size,"Unterminated string.",true)
        if(curly!=0)d+=RXDiagnostic(lines.size,"Unbalanced braces.",true)
        if(square!=0)d+=RXDiagnostic(lines.size,"Unbalanced brackets.",true)
        return RXValidationResult(d)
    }
}
