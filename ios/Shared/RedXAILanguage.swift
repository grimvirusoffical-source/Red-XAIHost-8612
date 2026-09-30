import Foundation

public enum RXValue: Equatable, Sendable {
    case number(Double), string(String), boolean(Bool), nell, array([RXValue])
}
public struct RXPacker: Identifiable, Equatable, Sendable {
    public let id = UUID()
    public var name:String; public var localID:Int?; public var value:RXValue; public var globalID:Int?; public var line:Int
}
public struct RXLanguageSnapshot: Sendable {
    public var packers:[RXPacker]; public var diagnostics:[RXDiagnostic]
}
public enum RedXAILanguage {
    public static func inspect(_ source:String)->RXLanguageSnapshot {
        var diagnostics=RedXAIValidator.validate(source).diagnostics
        var packers:[RXPacker]=[]; var locals=Set<String>(); var globals=Set<String>()
        for (offset,raw) in source.split(separator:"\n",omittingEmptySubsequences:false).enumerated() {
            let line=String(raw).trimmingCharacters(in:.whitespaces)
            guard line.contains("="),line.hasPrefix("{") else { continue }
            do {
                let p=try parsePacker(line,line:offset+1)
                if let id=p.localID { let k="\(p.name.lowercased())#\(id)"; if !locals.insert(k).inserted { diagnostics.append(.init(line:p.line,message:"Duplicate local Packer name/ID: \(p.name)[\(id)].",severity:.error)) } }
                if let id=p.globalID { let k="\(p.name.lowercased())#\(id)"; if !globals.insert(k).inserted { diagnostics.append(.init(line:p.line,message:"Duplicate global Packer name/ID: \(p.name)[\(id)].",severity:.error)) } }
                packers.append(p)
            } catch let e as ParseError { diagnostics.append(.init(line:offset+1,message:e.message,severity:.error)) }
            catch { diagnostics.append(.init(line:offset+1,message:"Could not parse Packer.",severity:.error)) }
        }
        return .init(packers:packers,diagnostics:diagnostics)
    }
    private struct ParseError:Error { let message:String }
    private static func parsePacker(_ line:String,line lineNumber:Int)throws->RXPacker {
        guard line.hasSuffix(",") else { throw ParseError(message:"Packer assignments must end with a comma.") }
        let body=String(line.dropLast()).trimmingCharacters(in:.whitespaces)
        guard let eq=body.firstIndex(of:"=") else { throw ParseError(message:"Missing '=' in Packer.") }
        let lhs=String(body[..<eq]).trimmingCharacters(in:.whitespaces)
        let rhs=String(body[body.index(after:eq)...]).trimmingCharacters(in:.whitespaces)
        guard lhs.first=="{",let close=lhs.firstIndex(of:"}") else { throw ParseError(message:"Invalid Packer name.") }
        let name=String(lhs[lhs.index(after:lhs.startIndex)..<close]); guard !name.isEmpty else { throw ParseError(message:"Packer name cannot be empty.") }
        let local=parseID(String(lhs[lhs.index(after:close)...]))
        guard rhs.first=="[",let valueEnd=matchingBracket(rhs) else { throw ParseError(message:"Invalid Packer value brackets.") }
        let raw=String(rhs[rhs.index(after:rhs.startIndex)..<valueEnd])
        let global=parseID(String(rhs[rhs.index(after:valueEnd)...]))
        return .init(name:name,localID:local,value:try parseValue(raw),globalID:global,line:lineNumber)
    }
    private static func parseID(_ text:String)->Int? {
        let t=text.trimmingCharacters(in:.whitespaces); guard t.hasPrefix("["),let end=t.firstIndex(of:"]") else{return nil}
        let raw=String(t[t.index(after:t.startIndex)..<end]).trimmingCharacters(in:.whitespaces)
        return raw.isEmpty || raw.lowercased()=="false" ? nil : Int(raw)
    }
    private static func matchingBracket(_ text:String)->String.Index? {
        var quoted=false,escaped=false,depth=0
        for i in text.indices { let ch=text[i]; if escaped{escaped=false;continue}; if ch=="\\"&&quoted{escaped=true;continue}; if ch=="\""{quoted.toggle();continue}; if quoted{continue}; if ch=="["{depth+=1}; if ch=="]"{depth-=1;if depth==0{return i}} }
        return nil
    }
    private static func parseValue(_ raw:String)throws->RXValue {
        let t=raw.trimmingCharacters(in:.whitespaces)
        if t.hasPrefix("\""),t.hasSuffix("\""),t.count>=2{return .string(String(t.dropFirst().dropLast()))}
        if t.lowercased()=="true"{return .boolean(true)}; if t.lowercased()=="false"{return .boolean(false)}; if t.lowercased()=="nell"{return .nell}
        if let n=Double(t){return .number(n)}
        if t.contains(","){return .array(try split(t).map(parseValue))}
        throw ParseError(message:"Unsupported or malformed value: \(t.prefix(40)).")
    }
    private static func split(_ text:String)->[String] {
        var out:[String]=[],cur="",quoted=false,escaped=false
        for ch in text { if escaped{cur.append(ch);escaped=false;continue}; if ch=="\\"&&quoted{cur.append(ch);escaped=true;continue}; if ch=="\""{quoted.toggle();cur.append(ch);continue}; if ch=="," &&  !quoted{out.append(cur);cur="";continue};cur.append(ch) }
        out.append(cur);return out
    }
}
